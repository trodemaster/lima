// SPDX-FileCopyrightText: Copyright The Lima Authors
// SPDX-License-Identifier: Apache-2.0

//go:build ignore

// Command gen builds tiny, structurally valid UDF-bridge ISO images for use
// as test fixtures, each containing a minimal WIM (header + XML metadata
// resource only, no actual Windows file data) at sources/install.wim or
// sources/install.esd.
//
// The XML resources embedded in these fixtures were all captured
// byte-for-byte from real Windows installer ISOs; see the .xml files in
// this directory, and the comment above each entry in main() for
// provenance. windows-server-2025-eval.xml came from the exact ISO
// templates/windows-2025.yaml downloads (https://aka.ms/WinServ2025iso-enus
// redirects to the Evaluation edition).
//
// Regenerate with: go run pkg/udfutil/testdata/gen/main.go
package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
)

const sectorSize = 2048

// Tag identifiers, matching pkg/udfutil's own constants (duplicated here
// since this is a standalone, build-ignored tool with no dependency on the
// package it's generating fixtures for).
const (
	tagAnchorVolumeDescriptorPointer = 2
	tagPartitionDescriptor           = 5
	tagLogicalVolumeDescriptor       = 6
	tagFileSetDescriptor             = 256
	tagFileIdentifierDescriptor      = 257
	tagFileEntry                     = 261
)

const (
	fileTypeDirectory = 4
	fileTypeRegular   = 5
)

const allocTypeShortAD = 0

// image describes one fixture to build.
type image struct {
	outputName string // written to ../<outputName>
	installAs  string // "install.wim" or "install.esd"
	xmlFile    string // source file under ./xml/
}

func main() {
	images := []image{
		// 2 editions (Enterprise, Professional): the ISO that started this
		// whole investigation -- a volume-license Windows 11 ARM64 ISO whose
		// non-"CCCOMA_" volume label used to be misdetected as Windows Server.
		{"windows11-client-2edition.iso", "install.wim", "windows11-client-2edition.xml"},
		// 8 editions, including non-Professional-first ordering: a MULTI
		// (multi-edition) retail-style Windows 11 ARM64 ISO.
		{"windows11-client-8edition.iso", "install.wim", "windows11-client-8edition.xml"},
		// 9 editions, install.esd instead of install.wim: a UUP-derived
		// Windows 11 ARM64 ISO. Exercises the install.wim -> install.esd
		// fallback path.
		{"windows11-client-9edition-esd.iso", "install.esd", "windows11-client-9edition-esd.xml"},
		// 1 edition (Professional): a small pre-release Windows 10 ARM64 ISO.
		{"windows10-client-1edition.iso", "install.wim", "windows10-client-1edition.xml"},
		// 4 editions (Standard/Datacenter x Core/Desktop Experience), from
		// the exact ISO templates/windows-2025.yaml downloads. Note
		// Standard's two images (Core and Desktop Experience) share the
		// same EDITIONID "ServerStandardEval" -- they're only
		// distinguished by INSTALLATIONTYPE/DISPLAYNAME -- as do
		// Datacenter's two images. This is real, observed behavior, not a
		// simplification: EDITIONID is not always unique per image.
		{"windows-server-2025-eval.iso", "install.wim", "windows-server-2025-eval.xml"},
	}

	outDir := "../"
	for _, img := range images {
		xmlPath := filepath.Join("xml", img.xmlFile)
		xmlBytes, err := os.ReadFile(xmlPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		isoBytes := buildISO(img.installAs, xmlBytes)
		outPath := filepath.Join(outDir, img.outputName)
		if err := os.WriteFile(outPath, isoBytes, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("%s: %d bytes (from %s, %d bytes of XML)\n", outPath, len(isoBytes), xmlPath, len(xmlBytes))
	}
}

// sectors is a growable collection of fixed-size sectors, indexed by
// absolute sector number.
type sectors struct {
	data [][]byte
}

func (s *sectors) at(n int) []byte {
	for len(s.data) <= n {
		s.data = append(s.data, make([]byte, sectorSize))
	}
	return s.data[n]
}

func (s *sectors) bytes() []byte {
	buf := make([]byte, len(s.data)*sectorSize)
	for i, sec := range s.data {
		copy(buf[i*sectorSize:], sec)
	}
	return buf
}

func putTag(b []byte, tagID uint16) {
	binary.LittleEndian.PutUint16(b[0:2], tagID)
}

// buildWIMFile constructs a minimal, structurally valid WIM file
// containing only a 208-byte header and an uncompressed XML metadata
// resource -- no image file data, since nothing that reads these fixtures
// needs it.
func buildWIMFile(xmlBytes []byte) []byte {
	const headerSize = 208
	header := make([]byte, headerSize)
	copy(header[0:8], []byte("MSWIM\x00\x00\x00"))
	binary.LittleEndian.PutUint32(header[8:12], headerSize)

	// XML resource header (RESHDR) at offset 72: packed size+flags(8),
	// offset(8, relative to the WIM file's own start), original size(8).
	const resourceFlagMetadata = 0x2
	sizeAndFlags := uint64(len(xmlBytes)) | uint64(resourceFlagMetadata)<<56
	binary.LittleEndian.PutUint64(header[72:80], sizeAndFlags)
	binary.LittleEndian.PutUint64(header[80:88], uint64(headerSize))
	binary.LittleEndian.PutUint64(header[88:96], uint64(len(xmlBytes)))

	return append(header, xmlBytes...)
}

// putFID writes one File Identifier Descriptor at data[offset:] and returns
// the offset of the next record. Only 8-bit (Latin-1) names are needed for
// these fixtures.
func putFID(data []byte, offset int, name string, icbLBN uint32, isParent bool) int {
	const fileCharParent = 0x8
	putTag(data[offset:], tagFileIdentifierDescriptor)
	var fileChar byte
	if isParent {
		fileChar = fileCharParent
	}
	data[offset+18] = fileChar
	nameBytes := append([]byte{8}, []byte(name)...)
	lfi := 0
	if name != "" {
		lfi = len(nameBytes)
	}
	data[offset+19] = byte(lfi)
	binary.LittleEndian.PutUint32(data[offset+24:offset+28], icbLBN)
	binary.LittleEndian.PutUint16(data[offset+36:offset+38], 0)
	if lfi > 0 {
		copy(data[offset+38:offset+38+lfi], nameBytes)
	}
	recLen := 38 + lfi
	return offset + (recLen+3)&^3
}

// buildISO assembles a minimal UDF-bridge ISO containing exactly one file,
// sources/<installAs>, whose content is a WIM/ESD file built from
// xmlBytes via buildWIMFile.
func buildISO(installAs string, xmlBytes []byte) []byte {
	s := &sectors{}
	const partStart = 32

	wimContent := buildWIMFile(xmlBytes)

	// AVDP at sector 256 -> Main VDS at sector 16, 2 sectors long.
	avdp := s.at(256)
	putTag(avdp, tagAnchorVolumeDescriptorPointer)
	binary.LittleEndian.PutUint32(avdp[16:20], sectorSize*2)
	binary.LittleEndian.PutUint32(avdp[20:24], 16)

	// Partition Descriptor at sector 16.
	pd := s.at(16)
	putTag(pd, tagPartitionDescriptor)
	binary.LittleEndian.PutUint32(pd[188:192], partStart)

	// Logical Volume Descriptor at sector 17 -> FSD at partition-relative lbn 0.
	lvd := s.at(17)
	putTag(lvd, tagLogicalVolumeDescriptor)
	binary.LittleEndian.PutUint32(lvd[212:216], sectorSize)
	binary.LittleEndian.PutUint32(lvd[248:252], sectorSize)
	binary.LittleEndian.PutUint32(lvd[252:256], 0)

	// File Set Descriptor at partition-relative lbn 0 -> root dir File Entry at lbn 1.
	fsd := s.at(partStart + 0)
	putTag(fsd, tagFileSetDescriptor)
	binary.LittleEndian.PutUint32(fsd[404:408], 1)

	// Root directory File Entry (lbn 1) -> its FIDs live at lbn 10.
	rootFE := s.at(partStart + 1)
	putTag(rootFE, tagFileEntry)
	rootFE[27] = fileTypeDirectory
	binary.LittleEndian.PutUint16(rootFE[34:36], allocTypeShortAD)
	rootData := s.at(partStart + 10)
	rootLen := 0
	rootLen = putFID(rootData, rootLen, "", 1, true)
	rootLen = putFID(rootData, rootLen, "sources", 2, false)
	binary.LittleEndian.PutUint64(rootFE[56:64], uint64(rootLen))
	binary.LittleEndian.PutUint32(rootFE[172:176], 8)
	binary.LittleEndian.PutUint32(rootFE[176:180], uint32(rootLen))
	binary.LittleEndian.PutUint32(rootFE[180:184], 10)

	// "sources" directory File Entry (lbn 2) -> its FIDs live at lbn 11.
	sourcesFE := s.at(partStart + 2)
	putTag(sourcesFE, tagFileEntry)
	sourcesFE[27] = fileTypeDirectory
	binary.LittleEndian.PutUint16(sourcesFE[34:36], allocTypeShortAD)
	sourcesData := s.at(partStart + 11)
	sourcesLen := 0
	sourcesLen = putFID(sourcesData, sourcesLen, "", 2, true)
	sourcesLen = putFID(sourcesData, sourcesLen, installAs, 3, false)
	binary.LittleEndian.PutUint64(sourcesFE[56:64], uint64(sourcesLen))
	binary.LittleEndian.PutUint32(sourcesFE[172:176], 8)
	binary.LittleEndian.PutUint32(sourcesFE[176:180], uint32(sourcesLen))
	binary.LittleEndian.PutUint32(sourcesFE[180:184], 11)

	// install.wim/install.esd File Entry (lbn 3) -> content starts at lbn 20.
	// A single extent is enough: every fixture's WIM content here is well
	// under short_ad's ~1GB single-extent limit.
	fileFE := s.at(partStart + 3)
	putTag(fileFE, tagFileEntry)
	fileFE[27] = fileTypeRegular
	binary.LittleEndian.PutUint16(fileFE[34:36], allocTypeShortAD)
	binary.LittleEndian.PutUint64(fileFE[56:64], uint64(len(wimContent)))
	binary.LittleEndian.PutUint32(fileFE[172:176], 8)
	binary.LittleEndian.PutUint32(fileFE[176:180], uint32(len(wimContent)))
	binary.LittleEndian.PutUint32(fileFE[180:184], 20)

	contentSectors := (len(wimContent) + sectorSize - 1) / sectorSize
	for i := range contentSectors {
		sec := s.at(partStart + 20 + i)
		start := i * sectorSize
		end := min(start+sectorSize, len(wimContent))
		copy(sec, wimContent[start:end])
	}

	return s.bytes()
}
