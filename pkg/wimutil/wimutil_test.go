// SPDX-FileCopyrightText: Copyright The Lima Authors
// SPDX-License-Identifier: Apache-2.0

package wimutil

import (
	"bytes"
	"encoding/binary"
	"testing"
	"unicode/utf16"

	"gotest.tools/v3/assert"
)

// buildFixture assembles a minimal, spec-shaped WIM file containing only a
// header and an uncompressed XML metadata resource, sufficient to exercise
// Images without needing a real multi-gigabyte WIM.
func buildFixture(t *testing.T, xmlText string) []byte {
	t.Helper()

	xmlBytes := encodeUTF16LE(xmlText)

	header := make([]byte, headerSize)
	copy(header[0:8], magic[:])
	binary.LittleEndian.PutUint32(header[8:12], headerSize)

	xmlOffset := uint64(headerSize)
	sizeAndFlags := uint64(len(xmlBytes)) | uint64(resourceFlagMetadata)<<56
	binary.LittleEndian.PutUint64(header[xmlResourceHeaderOffset:xmlResourceHeaderOffset+8], sizeAndFlags)
	binary.LittleEndian.PutUint64(header[xmlResourceHeaderOffset+8:xmlResourceHeaderOffset+16], xmlOffset)
	binary.LittleEndian.PutUint64(header[xmlResourceHeaderOffset+16:xmlResourceHeaderOffset+24], uint64(len(xmlBytes)))

	var buf bytes.Buffer
	buf.Write(header)
	buf.Write(xmlBytes)
	return buf.Bytes()
}

func encodeUTF16LE(s string) []byte {
	u16 := utf16.Encode([]rune(s))
	b := make([]byte, len(u16)*2)
	for i, v := range u16 {
		binary.LittleEndian.PutUint16(b[i*2:i*2+2], v)
	}
	return b
}

const sampleXML = `<WIM>` +
	`<IMAGE INDEX="1">` +
	`<WINDOWS><EDITIONID>Enterprise</EDITIONID><INSTALLATIONTYPE>Client</INSTALLATIONTYPE><PRODUCTTYPE>WinNT</PRODUCTTYPE></WINDOWS>` +
	`<DISPLAYNAME>Windows 11 Enterprise</DISPLAYNAME>` +
	`</IMAGE>` +
	`<IMAGE INDEX="2">` +
	`<WINDOWS><EDITIONID>Professional</EDITIONID><INSTALLATIONTYPE>Client</INSTALLATIONTYPE><PRODUCTTYPE>WinNT</PRODUCTTYPE></WINDOWS>` +
	`<DISPLAYNAME>Windows 11 Pro</DISPLAYNAME>` +
	`</IMAGE>` +
	`<IMAGE INDEX="3">` +
	`<WINDOWS><EDITIONID>ServerStandard</EDITIONID><INSTALLATIONTYPE>Server Core</INSTALLATIONTYPE><PRODUCTTYPE>ServerNT</PRODUCTTYPE></WINDOWS>` +
	`<DISPLAYNAME>Windows Server 2025 Standard</DISPLAYNAME>` +
	`</IMAGE>` +
	`</WIM>`

func TestImages(t *testing.T) {
	data := buildFixture(t, sampleXML)
	images, err := Images(bytes.NewReader(data))
	assert.NilError(t, err)
	assert.Equal(t, len(images), 3)

	assert.Equal(t, images[0].Index, 1)
	assert.Equal(t, images[0].EditionID, "Enterprise")
	assert.Equal(t, images[0].DisplayName, "Windows 11 Enterprise")
	assert.Equal(t, images[0].IsServer(), false)

	assert.Equal(t, images[1].EditionID, "Professional")
	assert.Equal(t, images[1].IsServer(), false)

	assert.Equal(t, images[2].EditionID, "ServerStandard")
	assert.Equal(t, images[2].ProductType, "ServerNT")
	assert.Equal(t, images[2].IsServer(), true)
}

func TestImagesBadSignature(t *testing.T) {
	data := buildFixture(t, sampleXML)
	copy(data[0:8], []byte("NOTAWIM\x00"))
	_, err := Images(bytes.NewReader(data))
	assert.ErrorContains(t, err, "bad signature")
}

func TestImagesCompressedMetadata(t *testing.T) {
	data := buildFixture(t, sampleXML)
	// Clear the metadata flag to simulate a compressed (ESD-style) resource.
	sizeAndFlags := binary.LittleEndian.Uint64(data[xmlResourceHeaderOffset : xmlResourceHeaderOffset+8])
	sizeAndFlags &^= uint64(resourceFlagMetadata) << 56
	binary.LittleEndian.PutUint64(data[xmlResourceHeaderOffset:xmlResourceHeaderOffset+8], sizeAndFlags)

	_, err := Images(bytes.NewReader(data))
	assert.ErrorIs(t, err, ErrCompressedMetadata)
}

// TestIsServerFallsBackToProductType covers older WIMs that carry
// PRODUCTTYPE but not INSTALLATIONTYPE.
func TestIsServerFallsBackToProductType(t *testing.T) {
	client := Image{ProductType: "WinNT"}
	assert.Equal(t, client.IsServer(), false)

	server := Image{ProductType: "ServerNT"}
	assert.Equal(t, server.IsServer(), true)

	unknown := Image{}
	assert.Equal(t, unknown.IsServer(), false)
}
