// SPDX-FileCopyrightText: Copyright The Lima Authors
// SPDX-License-Identifier: Apache-2.0

package udfutil

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/lima-vm/lima/v2/pkg/wimutil"
)

// fixtureBuilder assembles a minimal, spec-shaped UDF volume: an AVDP, a
// two-descriptor Main VDS (Partition Descriptor + Logical Volume
// Descriptor), a File Set Descriptor, a root directory with one
// subdirectory and one multi-extent file, laid out sector by sector. It
// exercises the same structures validated by hand against a real Windows
// installer ISO, without needing a multi-gigabyte fixture file.
type fixtureBuilder struct {
	sectors [][]byte
}

func newFixtureBuilder() *fixtureBuilder {
	return &fixtureBuilder{}
}

// at returns a zeroed sector at the given absolute sector number, growing
// the backing store as needed, and records it for later retrieval.
func (b *fixtureBuilder) at(sector int) []byte {
	for len(b.sectors) <= sector {
		b.sectors = append(b.sectors, make([]byte, sectorSize))
	}
	return b.sectors[sector]
}

func (b *fixtureBuilder) bytes() []byte {
	buf := make([]byte, len(b.sectors)*sectorSize)
	for i, s := range b.sectors {
		copy(buf[i*sectorSize:], s)
	}
	return buf
}

func putTag(b []byte, tagID uint16) {
	binary.LittleEndian.PutUint16(b[0:2], tagID)
}

// putFID writes one File Identifier Descriptor at data[offset:] and
// returns the offset of the next record.
func putFID(data []byte, offset int, name string, icbLBN uint32, isParent bool) int {
	putTag(data[offset:], tagFileIdentifierDescriptor)
	var fileChar byte
	if isParent {
		fileChar = fileCharParent
	}
	data[offset+18] = fileChar
	nameBytes := append([]byte{8}, []byte(name)...) // compID 8 = 8-bit
	lfi := 0
	if name != "" {
		lfi = len(nameBytes)
	}
	data[offset+19] = byte(lfi)
	binary.LittleEndian.PutUint32(data[offset+24:offset+28], icbLBN) // ICB long_ad LBN
	binary.LittleEndian.PutUint16(data[offset+36:offset+38], 0)      // LengthOfImplementationUse
	if lfi > 0 {
		copy(data[offset+38:offset+38+lfi], nameBytes)
	}
	recLen := 38 + lfi
	return offset + (recLen+3)&^3
}

// buildUDFFixture returns a UDF image (as an io.ReaderAt) containing:
//
//	/            (root, ICB at partition-relative lbn 0)
//	/subdir      (directory, lbn 1)
//	/subdir/big  (regular file, lbn 2, split across two extents)
func buildUDFFixture(t *testing.T) ([]byte, []byte) {
	t.Helper()
	b := newFixtureBuilder()

	const partStart = 32 // arbitrary absolute start sector for the partition

	// AVDP at sector 256.
	avdp := b.at(256)
	putTag(avdp, tagAnchorVolumeDescriptorPointer)
	binary.LittleEndian.PutUint32(avdp[16:20], sectorSize*2) // Main VDS length: 2 sectors
	binary.LittleEndian.PutUint32(avdp[20:24], 16)           // Main VDS location: sector 16

	// Main VDS: Partition Descriptor at sector 16, Logical Volume Descriptor at 17.
	pd := b.at(16)
	putTag(pd, tagPartitionDescriptor)
	binary.LittleEndian.PutUint32(pd[188:192], partStart)

	lvd := b.at(17)
	putTag(lvd, tagLogicalVolumeDescriptor)
	binary.LittleEndian.PutUint32(lvd[212:216], sectorSize)
	// LogicalVolumeContentsUse: FSD long_ad, length=1 sector, lbn=0 (partition-relative).
	binary.LittleEndian.PutUint32(lvd[248:252], sectorSize)
	binary.LittleEndian.PutUint32(lvd[252:256], 0)

	// File Set Descriptor at partition-relative lbn 0 (absolute partStart+0).
	fsd := b.at(partStart + 0)
	putTag(fsd, tagFileSetDescriptor)
	binary.LittleEndian.PutUint32(fsd[404:408], 1) // RootDirectoryICB lbn=1 (partition-relative)

	// Root directory File Entry at partition-relative lbn 1.
	rootFE := b.at(partStart + 1)
	putTag(rootFE, tagFileEntry)
	rootFE[27] = fileTypeDirectory
	binary.LittleEndian.PutUint16(rootFE[34:36], allocTypeShortAD)
	rootData := b.at(partStart + 10) // root directory content extent
	rootLen := 0
	rootLen = putFID(rootData, rootLen, "", 1, true)        // parent/self entry
	rootLen = putFID(rootData, rootLen, "subdir", 2, false) // -> lbn 2
	binary.LittleEndian.PutUint64(rootFE[56:64], uint64(rootLen))
	binary.LittleEndian.PutUint32(rootFE[168:172], 0)               // no extended attributes
	binary.LittleEndian.PutUint32(rootFE[172:176], 8)               // one short_ad (8 bytes)
	binary.LittleEndian.PutUint32(rootFE[176:180], uint32(rootLen)) // extent length (type bits 0)
	binary.LittleEndian.PutUint32(rootFE[180:184], 10)              // extent lbn (partition-relative)

	// subdir File Entry at partition-relative lbn 2.
	subFE := b.at(partStart + 2)
	putTag(subFE, tagFileEntry)
	subFE[27] = fileTypeDirectory
	binary.LittleEndian.PutUint16(subFE[34:36], allocTypeShortAD)
	subData := b.at(partStart + 11)
	subLen := 0
	subLen = putFID(subData, subLen, "", 2, true)
	subLen = putFID(subData, subLen, "big", 3, false) // -> lbn 3
	binary.LittleEndian.PutUint64(subFE[56:64], uint64(subLen))
	binary.LittleEndian.PutUint32(subFE[168:172], 0)
	binary.LittleEndian.PutUint32(subFE[172:176], 8)
	binary.LittleEndian.PutUint32(subFE[176:180], uint32(subLen))
	binary.LittleEndian.PutUint32(subFE[180:184], 11)

	// "big" File Entry at partition-relative lbn 3: a regular file split
	// across two extents, to exercise extentReader's multi-extent path.
	bigFE := b.at(partStart + 3)
	putTag(bigFE, tagFileEntry)
	bigFE[27] = fileTypeRegular
	binary.LittleEndian.PutUint16(bigFE[34:36], allocTypeShortAD)
	content := bytes.Repeat([]byte("A"), sectorSize)
	content = append(content, bytes.Repeat([]byte("B"), 100)...)
	ext0Len := sectorSize
	ext1Len := 100
	binary.LittleEndian.PutUint64(bigFE[56:64], uint64(len(content)))
	binary.LittleEndian.PutUint32(bigFE[168:172], 0)
	binary.LittleEndian.PutUint32(bigFE[172:176], 16) // two short_ad entries
	binary.LittleEndian.PutUint32(bigFE[176:180], uint32(ext0Len))
	binary.LittleEndian.PutUint32(bigFE[180:184], 20) // extent 0 at lbn 20
	binary.LittleEndian.PutUint32(bigFE[184:188], uint32(ext1Len))
	binary.LittleEndian.PutUint32(bigFE[188:192], 21) // extent 1 at lbn 21
	copy(b.at(partStart+20), content[:ext0Len])
	copy(b.at(partStart+21), content[ext0Len:])

	return b.bytes(), content
}

func TestOpenNestedFile(t *testing.T) {
	image, wantContent := buildUDFFixture(t)
	r, err := Open(bytes.NewReader(image), "subdir/big")
	assert.NilError(t, err)

	got, err := io.ReadAll(r)
	assert.NilError(t, err)
	assert.DeepEqual(t, got, wantContent)
}

func TestOpenSeek(t *testing.T) {
	image, wantContent := buildUDFFixture(t)
	r, err := Open(bytes.NewReader(image), "/subdir/big")
	assert.NilError(t, err)

	// Seek to a position inside the second extent and verify the byte
	// there, exercising the extent-boundary-crossing path.
	off, err := r.Seek(int64(sectorSize+5), io.SeekStart)
	assert.NilError(t, err)
	assert.Equal(t, off, int64(sectorSize+5))

	got := make([]byte, 10)
	n, err := r.Read(got)
	assert.NilError(t, err)
	assert.Equal(t, n, 10)
	assert.DeepEqual(t, got, wantContent[sectorSize+5:sectorSize+15])
}

func TestOpenNotFound(t *testing.T) {
	image, _ := buildUDFFixture(t)
	_, err := Open(bytes.NewReader(image), "subdir/missing")
	assert.ErrorContains(t, err, "no such file or directory")
}

func TestOpenNotAFile(t *testing.T) {
	image, _ := buildUDFFixture(t)
	_, err := Open(bytes.NewReader(image), "subdir")
	assert.ErrorContains(t, err, "not a regular file")
}

// TestOpenTestdataFixtures is a lighter package-local sanity check that
// Open works against the real ISO-shaped fixtures in testdata/ (built by
// testdata/gen), complementing the hand-built synthetic image above, which
// exists to exercise UDF mechanics (multi-extent stitching, error paths)
// precisely rather than to look like a real disc. See pkg/cidata's own
// tests for the fuller edition-selection matrix these fixtures back.
func TestOpenTestdataFixtures(t *testing.T) {
	testCases := []struct {
		fixture   string
		installAs string
		wantCount int
	}{
		{"windows11-client-2edition.iso", "sources/install.wim", 2},
		{"windows11-client-9edition-esd.iso", "sources/install.esd", 9},
	}
	for _, tc := range testCases {
		t.Run(tc.fixture, func(t *testing.T) {
			f, err := os.Open(filepath.Join("testdata", tc.fixture))
			assert.NilError(t, err)
			defer f.Close()

			r, err := Open(f, tc.installAs)
			assert.NilError(t, err)

			images, err := wimutil.Images(r)
			assert.NilError(t, err)
			assert.Equal(t, len(images), tc.wantCount)
		})
	}
}

func TestOpenBadSignature(t *testing.T) {
	image, _ := buildUDFFixture(t)
	// Corrupt the AVDP tag.
	binary.LittleEndian.PutUint16(image[256*sectorSize:], 0)
	_, err := Open(bytes.NewReader(image), "subdir/big")
	assert.ErrorContains(t, err, "not a UDF volume")
}
