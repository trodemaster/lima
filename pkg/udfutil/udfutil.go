// SPDX-FileCopyrightText: Copyright The Lima Authors
// SPDX-License-Identifier: Apache-2.0

// Package udfutil resolves a single named file inside a UDF (ECMA-167)
// filesystem, such as the UDF bridge partition on a modern Windows
// installer ISO whose sources/install.wim exceeds ISO9660's 4GiB
// single-file limit.
//
// It implements only the minimal subset of UDF needed to walk a path from
// the volume root to a target file and read that file's content: the
// Anchor Volume Descriptor Pointer, the Main Volume Descriptor Sequence's
// Partition Descriptor and Logical Volume Descriptor, the File Set
// Descriptor, and File Entry / File Identifier Descriptor traversal. It
// deliberately does not support multi-partition volumes, Extended File
// Entries, embedded (in-ICB) file data, or ext_ad allocation descriptors —
// none of which appear on the optical-media Windows installer ISOs this
// package targets. Unsupported structures produce a clear error rather
// than being silently misread.
package udfutil

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf16"
)

const sectorSize = 2048

// Descriptor tag identifiers (ECMA-167 3rd ed., section 3.2/14.2) relevant
// to path resolution.
const (
	tagAnchorVolumeDescriptorPointer = 2
	tagPartitionDescriptor           = 5
	tagLogicalVolumeDescriptor       = 6
	tagTerminatingDescriptor         = 8
	tagFileSetDescriptor             = 256
	tagFileIdentifierDescriptor      = 257
	tagFileEntry                     = 261
)

// FileIdentifierDescriptor.FileCharacteristics bits (ECMA-167 14.4.3).
const fileCharParent = 0x8

// ICBTag.FileType values (ECMA-167 14.6.6) relevant to path resolution.
const (
	fileTypeDirectory = 4
	fileTypeRegular   = 5
)

// ICBTag.Flags allocation descriptor type, packed into the low 3 bits
// (ECMA-167 14.6.8).
const (
	allocTypeShortAD = 0
	allocTypeLongAD  = 1
)

// extentLengthMask isolates the low 30 bits of a packed allocation
// descriptor length field; the top 2 bits encode the extent's allocation
// state (recorded/allocated, unrecorded, unallocated, or a continuation
// pointer).
const extentLengthMask = 0x3FFFFFFF

// ErrUnsupported is returned when a volume uses a UDF structure this
// package deliberately does not implement (see the package doc comment).
var ErrUnsupported = errors.New("unsupported UDF structure")

type extent struct {
	lbn    uint32 // partition-relative logical block number
	length uint32 // bytes
}

type partitionInfo struct {
	start uint32 // absolute logical block number of the partition's first block
}

type fileEntryInfo struct {
	fileType uint8
	length   int64
	extents  []extent
}

// Open resolves the slash-separated path within the UDF filesystem read
// from r and returns a seekable reader over the target file's content.
func Open(r io.ReaderAt, path string) (io.ReadSeeker, error) {
	part, fe, err := readVolume(r)
	if err != nil {
		return nil, err
	}

	for name := range strings.SplitSeq(strings.Trim(path, "/"), "/") {
		if name == "" {
			continue
		}
		if fe.fileType != fileTypeDirectory {
			return nil, fmt.Errorf("udfutil: %#q: not a directory", path)
		}
		children, err := readDirectory(r, part, fe)
		if err != nil {
			return nil, err
		}
		lbn, ok := children[strings.ToLower(name)]
		if !ok {
			return nil, fmt.Errorf("udfutil: %#q: no such file or directory", path)
		}
		fe, err = readFileEntry(r, part, lbn)
		if err != nil {
			return nil, err
		}
	}
	if fe.fileType != fileTypeRegular {
		return nil, fmt.Errorf("udfutil: %#q: not a regular file", path)
	}

	return newExtentReader(r, part, fe.extents, fe.length), nil
}

// readVolume walks the Anchor Volume Descriptor Pointer, Main Volume
// Descriptor Sequence, and File Set Descriptor to find the volume's single
// partition and its root directory's File Entry.
func readVolume(r io.ReaderAt) (partitionInfo, fileEntryInfo, error) {
	avdp, err := readAt(r, 256*sectorSize, sectorSize)
	if err != nil {
		return partitionInfo{}, fileEntryInfo{}, fmt.Errorf("udfutil: failed to read AVDP: %w", err)
	}
	if tagID(avdp) != tagAnchorVolumeDescriptorPointer {
		return partitionInfo{}, fileEntryInfo{}, errors.New("udfutil: not a UDF volume (bad AVDP tag)")
	}
	// MainVolumeDescriptorSequenceExtent (extent_ad): Length(4) at 16, Location(4) at 20.
	mvdsLen := binary.LittleEndian.Uint32(avdp[16:20])
	mvdsLoc := binary.LittleEndian.Uint32(avdp[20:24])

	var part partitionInfo
	var havePart bool
	var fsdExt extent
	var haveFSD bool

	numSectors := int((mvdsLen + sectorSize - 1) / sectorSize)
sequenceLoop:
	for i := range numSectors {
		sec, err := readAt(r, (int64(mvdsLoc)+int64(i))*sectorSize, sectorSize)
		if err != nil {
			return partitionInfo{}, fileEntryInfo{}, fmt.Errorf("udfutil: failed to read Main VDS: %w", err)
		}
		switch tagID(sec) {
		case tagTerminatingDescriptor:
			break sequenceLoop
		case tagPartitionDescriptor:
			if havePart {
				return partitionInfo{}, fileEntryInfo{}, fmt.Errorf("udfutil: %w: multiple partitions", ErrUnsupported)
			}
			// PartitionStartingLocation(4) at offset 188.
			part = partitionInfo{start: binary.LittleEndian.Uint32(sec[188:192])}
			havePart = true
		case tagLogicalVolumeDescriptor:
			// LogicalBlockSize(4) at offset 212.
			if blockSize := binary.LittleEndian.Uint32(sec[212:216]); blockSize != sectorSize {
				return partitionInfo{}, fileEntryInfo{}, fmt.Errorf("udfutil: %w: logical block size %d", ErrUnsupported, blockSize)
			}
			// LogicalVolumeContentsUse (16 bytes) at offset 248 holds the File Set
			// Descriptor sequence's long_ad: Length(4) + LBN(4) + PartitionRef(2) + ImplementationUse(6).
			lvcu := sec[248:264]
			fsdExt = extent{
				length: binary.LittleEndian.Uint32(lvcu[0:4]),
				lbn:    binary.LittleEndian.Uint32(lvcu[4:8]),
			}
			haveFSD = true
		}
	}
	if !havePart {
		return partitionInfo{}, fileEntryInfo{}, errors.New("udfutil: no Partition Descriptor found")
	}
	if !haveFSD {
		return partitionInfo{}, fileEntryInfo{}, errors.New("udfutil: no Logical Volume Descriptor found")
	}

	fsd, err := readAt(r, int64(part.start+fsdExt.lbn)*sectorSize, sectorSize)
	if err != nil {
		return partitionInfo{}, fileEntryInfo{}, fmt.Errorf("udfutil: failed to read File Set Descriptor: %w", err)
	}
	if tagID(fsd) != tagFileSetDescriptor {
		return partitionInfo{}, fileEntryInfo{}, errors.New("udfutil: expected File Set Descriptor")
	}
	// RootDirectoryICB (long_ad) at offset 400: Length(4) + LBN(4) + PartitionRef(2).
	rootLBN := binary.LittleEndian.Uint32(fsd[404:408])

	rootFE, err := readFileEntry(r, part, rootLBN)
	if err != nil {
		return partitionInfo{}, fileEntryInfo{}, err
	}
	return part, rootFE, nil
}

// readFileEntry reads and parses the File Entry at partition-relative
// logical block lbn, returning its type, size, and data extents.
func readFileEntry(r io.ReaderAt, part partitionInfo, lbn uint32) (fileEntryInfo, error) {
	absOff := int64(part.start+lbn) * sectorSize
	head, err := readAt(r, absOff, sectorSize)
	if err != nil {
		return fileEntryInfo{}, fmt.Errorf("udfutil: failed to read File Entry: %w", err)
	}
	if tagID(head) != tagFileEntry {
		return fileEntryInfo{}, fmt.Errorf("udfutil: %w: expected File Entry, got tag %d (Extended File Entries are not supported)", ErrUnsupported, tagID(head))
	}

	// ICBTag begins at offset 16; within it, FileType is at 16+11=27 and
	// Flags (whose low 3 bits give the allocation descriptor type) is at
	// 16+18=34 (ECMA-167 14.6).
	fileType := head[27]
	allocType := binary.LittleEndian.Uint16(head[34:36]) & 0x7
	// InformationLength(8) at offset 56.
	length := int64(binary.LittleEndian.Uint64(head[56:64]))
	// LengthOfExtendedAttributes(4) at 168, LengthOfAllocationDescriptors(4) at 172.
	lenExtAttrs := int(binary.LittleEndian.Uint32(head[168:172]))
	lenAllocDescs := int(binary.LittleEndian.Uint32(head[172:176]))
	adStart := 176 + lenExtAttrs

	buf := head
	if needed := adStart + lenAllocDescs; needed > sectorSize {
		numSectors := (needed + sectorSize - 1) / sectorSize
		buf, err = readAt(r, absOff, numSectors*sectorSize)
		if err != nil {
			return fileEntryInfo{}, fmt.Errorf("udfutil: failed to read File Entry allocation descriptors: %w", err)
		}
	}

	var extents []extent
	switch allocType {
	case allocTypeShortAD:
		for off := adStart; off < adStart+lenAllocDescs; off += 8 {
			raw := binary.LittleEndian.Uint32(buf[off : off+4])
			if extType := raw >> 30; extType != 0 {
				return fileEntryInfo{}, fmt.Errorf("udfutil: %w: extent allocation type %d", ErrUnsupported, extType)
			}
			extents = append(extents, extent{
				length: raw & extentLengthMask,
				lbn:    binary.LittleEndian.Uint32(buf[off+4 : off+8]),
			})
		}
	case allocTypeLongAD:
		for off := adStart; off < adStart+lenAllocDescs; off += 16 {
			raw := binary.LittleEndian.Uint32(buf[off : off+4])
			if extType := raw >> 30; extType != 0 {
				return fileEntryInfo{}, fmt.Errorf("udfutil: %w: extent allocation type %d", ErrUnsupported, extType)
			}
			extents = append(extents, extent{
				length: raw & extentLengthMask,
				lbn:    binary.LittleEndian.Uint32(buf[off+4 : off+8]),
			})
		}
	default:
		return fileEntryInfo{}, fmt.Errorf("udfutil: %w: allocation descriptor type %d", ErrUnsupported, allocType)
	}

	return fileEntryInfo{fileType: fileType, length: length, extents: extents}, nil
}

// readDirectory reads a directory's File Identifier Descriptors and
// returns a map of lowercased child name to that child's File Entry
// logical block number.
func readDirectory(r io.ReaderAt, part partitionInfo, dir fileEntryInfo) (map[string]uint32, error) {
	data, err := readExtents(r, part, dir.extents, dir.length)
	if err != nil {
		return nil, err
	}

	entries := make(map[string]uint32)
	offset := 0
	for offset+38 <= len(data) && tagID(data[offset:]) == tagFileIdentifierDescriptor {
		fileChar := data[offset+18]
		lfi := int(data[offset+19])
		// ICB (long_ad) at offset+20: Length(4) + LBN(4) at offset+24.
		icbLBN := binary.LittleEndian.Uint32(data[offset+24 : offset+28])
		liu := int(binary.LittleEndian.Uint16(data[offset+36 : offset+38]))
		nameOff := offset + 38 + liu
		if nameOff+lfi > len(data) {
			return nil, errors.New("udfutil: truncated File Identifier Descriptor")
		}
		name := decodeFileIdentifier(data[nameOff : nameOff+lfi])
		if fileChar&fileCharParent == 0 && name != "" {
			entries[strings.ToLower(name)] = icbLBN
		}
		recLen := 38 + liu + lfi
		offset += (recLen + 3) &^ 3 // pad to 4-byte boundary
	}
	return entries, nil
}

// decodeFileIdentifier decodes a UDF FileIdentifier field (OSTA compressed
// unicode): the first byte selects 8-bit (Latin-1 code points) or 16-bit
// big-endian encoding for the remaining bytes.
func decodeFileIdentifier(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	compID, payload := b[0], b[1:]
	switch compID {
	case 8:
		runes := make([]rune, len(payload))
		for i, c := range payload {
			runes[i] = rune(c)
		}
		return string(runes)
	case 16:
		u16 := make([]uint16, len(payload)/2)
		for i := range u16 {
			u16[i] = binary.BigEndian.Uint16(payload[i*2 : i*2+2])
		}
		return string(utf16.Decode(u16))
	default:
		return ""
	}
}

// readExtents reads and concatenates a file's data extents. It is only
// used for directories, which are small; large files are instead read
// lazily through an extentReader.
func readExtents(r io.ReaderAt, part partitionInfo, exts []extent, total int64) ([]byte, error) {
	buf := make([]byte, 0, total)
	for _, e := range exts {
		chunk, err := readAt(r, int64(part.start+e.lbn)*sectorSize, int(e.length))
		if err != nil {
			return nil, fmt.Errorf("udfutil: failed to read extent: %w", err)
		}
		buf = append(buf, chunk...)
	}
	if int64(len(buf)) < total {
		return nil, fmt.Errorf("udfutil: extents shorter than expected (%d < %d bytes)", len(buf), total)
	}
	return buf[:total], nil
}

func tagID(b []byte) uint16 {
	if len(b) < 2 {
		return 0
	}
	return binary.LittleEndian.Uint16(b[0:2])
}

func readAt(r io.ReaderAt, off int64, n int) ([]byte, error) {
	buf := make([]byte, n)
	if _, err := r.ReadAt(buf, off); err != nil {
		return nil, err
	}
	return buf, nil
}

// extentReader is a lazy, seekable view over a file's (possibly
// discontiguous) UDF extents, without reading them into memory up front.
type extentReader struct {
	r       io.ReaderAt
	offsets []int64 // absolute byte offset of each extent
	lengths []int64 // byte length of each extent
	total   int64
	pos     int64
}

func newExtentReader(r io.ReaderAt, part partitionInfo, exts []extent, total int64) *extentReader {
	offsets := make([]int64, len(exts))
	lengths := make([]int64, len(exts))
	for i, e := range exts {
		offsets[i] = int64(part.start+e.lbn) * sectorSize
		lengths[i] = int64(e.length)
	}
	return &extentReader{r: r, offsets: offsets, lengths: lengths, total: total}
}

func (er *extentReader) Seek(offset int64, whence int) (int64, error) {
	var newPos int64
	switch whence {
	case io.SeekStart:
		newPos = offset
	case io.SeekCurrent:
		newPos = er.pos + offset
	case io.SeekEnd:
		newPos = er.total + offset
	default:
		return 0, errors.New("udfutil: invalid whence")
	}
	if newPos < 0 {
		return 0, errors.New("udfutil: negative seek position")
	}
	er.pos = newPos
	return er.pos, nil
}

func (er *extentReader) Read(p []byte) (int, error) {
	if er.pos >= er.total {
		return 0, io.EOF
	}
	var extStart int64
	for i, length := range er.lengths {
		extEnd := extStart + length
		if er.pos < extEnd {
			within := er.pos - extStart
			toRead := min(length-within, int64(len(p)), er.total-er.pos)
			n, err := er.r.ReadAt(p[:toRead], er.offsets[i]+within)
			er.pos += int64(n)
			return n, err
		}
		extStart = extEnd
	}
	return 0, io.EOF
}
