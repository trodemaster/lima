// SPDX-FileCopyrightText: Copyright The Lima Authors
// SPDX-License-Identifier: Apache-2.0

// Package wimutil reads the image list embedded in a Windows Imaging Format
// (WIM) file, such as sources/install.wim on a Windows installer ISO.
//
// It only parses the WIM header and its XML metadata resource; it does not
// extract or decompress any image file data. That is sufficient to enumerate
// the editions available in a multi-image WIM (their /IMAGE/INDEX, EDITIONID,
// DISPLAYNAME, and whether they are Windows client or server installations)
// without needing a full WIM implementation. The XML resource is always
// stored uncompressed in a WIM (unlike the ESD format, which this package
// does not support); ErrCompressedMetadata is returned when that assumption
// doesn't hold.
package wimutil

import (
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"unicode/utf16"
)

// magic is the fixed 8-byte signature at the start of every WIM file.
var magic = [8]byte{'M', 'S', 'W', 'I', 'M', 0, 0, 0}

const headerSize = 208

// xmlResourceHeaderOffset is the fixed byte offset of the XML data resource
// header within the 208-byte WIM header.
const xmlResourceHeaderOffset = 72

// resourceHeaderSize is the size, in bytes, of a packed WIM resource header
// (RESHDR): an 8-byte packed size+flags field, an 8-byte offset, and an
// 8-byte original size.
const resourceHeaderSize = 24

// resourceFlagMetadata marks a resource header entry as uncompressed
// metadata, as opposed to (possibly compressed) image file data. The XML
// image list is always stored with this flag set and without compression.
const resourceFlagMetadata = 0x2

// ErrCompressedMetadata is returned when the WIM's XML metadata resource is
// not stored uncompressed, which this package cannot read. This is expected
// for the ESD format, which is not supported.
var ErrCompressedMetadata = errors.New("WIM XML metadata resource is not stored uncompressed (ESD files are not supported)")

// maxXMLResourceSize is a sanity bound on the XML metadata resource size.
// Real WIM XML resources are a few kilobytes per image; this is generous
// enough to allow for WIMs with many images while still rejecting a
// corrupt or misidentified header before attempting a large allocation.
const maxXMLResourceSize = 16 << 20

// Image describes one entry in a WIM's XML image list.
type Image struct {
	Index              int    `xml:"INDEX,attr"`
	EditionID          string `xml:"WINDOWS>EDITIONID"`
	InstallationType   string `xml:"WINDOWS>INSTALLATIONTYPE"`
	ProductType        string `xml:"WINDOWS>PRODUCTTYPE"`
	DisplayName        string `xml:"DISPLAYNAME"`
	DisplayDescription string `xml:"DISPLAYDESCRIPTION"`
}

// IsServer reports whether the image is a Windows Server installation. It
// prefers INSTALLATIONTYPE ("Client" vs. "Server..."), falling back to
// PRODUCTTYPE ("WinNT" vs. "ServerNT"/"LanmanNT") for older WIMs that don't
// carry INSTALLATIONTYPE.
func (img Image) IsServer() bool {
	if img.InstallationType != "" {
		return img.InstallationType != "Client"
	}
	return img.ProductType != "" && img.ProductType != "WinNT"
}

type imageList struct {
	Images []Image `xml:"IMAGE"`
}

type resourceHeader struct {
	Size         uint64
	Flags        uint8
	Offset       uint64
	OriginalSize uint64
}

// readResourceHeader unpacks a 24-byte WIM RESHDR structure. The first 8
// bytes pack a 56-bit little-endian size with an 8-bit flags byte in the
// top byte.
func readResourceHeader(b []byte) resourceHeader {
	sizeAndFlags := binary.LittleEndian.Uint64(b[0:8])
	return resourceHeader{
		Size:         sizeAndFlags & 0x00FFFFFFFFFFFFFF,
		Flags:        uint8(sizeAndFlags >> 56),
		Offset:       binary.LittleEndian.Uint64(b[8:16]),
		OriginalSize: binary.LittleEndian.Uint64(b[16:24]),
	}
}

// Images reads and returns the image list embedded in a WIM file.
func Images(r io.ReadSeeker) ([]Image, error) {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	header := make([]byte, headerSize)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, fmt.Errorf("failed to read WIM header: %w", err)
	}
	if [8]byte(header[0:8]) != magic {
		return nil, errors.New("not a WIM file (bad signature)")
	}

	xmlRes := readResourceHeader(header[xmlResourceHeaderOffset : xmlResourceHeaderOffset+resourceHeaderSize])
	if xmlRes.Flags&resourceFlagMetadata == 0 {
		return nil, ErrCompressedMetadata
	}
	if xmlRes.Size == 0 || xmlRes.Size > maxXMLResourceSize {
		return nil, fmt.Errorf("WIM XML metadata resource has implausible size %d", xmlRes.Size)
	}

	if _, err := r.Seek(int64(xmlRes.Offset), io.SeekStart); err != nil {
		return nil, err
	}
	rawXML := make([]byte, xmlRes.Size)
	if _, err := io.ReadFull(r, rawXML); err != nil {
		return nil, fmt.Errorf("failed to read WIM XML metadata: %w", err)
	}

	text, err := decodeUTF16LE(rawXML)
	if err != nil {
		return nil, fmt.Errorf("failed to decode WIM XML metadata: %w", err)
	}

	var list imageList
	if err := xml.Unmarshal([]byte(text), &list); err != nil {
		return nil, fmt.Errorf("failed to parse WIM XML metadata: %w", err)
	}
	return list.Images, nil
}

// decodeUTF16LE decodes a UTF-16LE byte string, as used by the WIM XML
// metadata resource, stripping a leading byte-order mark if present.
func decodeUTF16LE(b []byte) (string, error) {
	if len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE {
		b = b[2:]
	}
	if len(b)%2 != 0 {
		return "", errors.New("invalid UTF-16LE data: odd byte length")
	}
	u16 := make([]uint16, len(b)/2)
	for i := range u16 {
		u16[i] = binary.LittleEndian.Uint16(b[i*2 : i*2+2])
	}
	return string(utf16.Decode(u16)), nil
}
