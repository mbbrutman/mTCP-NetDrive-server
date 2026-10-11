/*

   mTCP NetDrive Server (bpb.go)
   Copyright (C) 2023-2026 Michael B. Brutman (mbbrutman@gmail.com)
   mTCP web page: http://www.brutman.com/mTCP/mTCP.html


   This file is part of mTCP.

   mTCP is free software: you can redistribute it and/or modify
   it under the terms of the GNU General Public License as published by
   the Free Software Foundation, either version 3 of the License, or
   (at your option) any later version.

   mTCP is distributed in the hope that it will be useful,
   but WITHOUT ANY WARRANTY; without even the implied warranty of
   MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
   GNU General Public License for more details.

   You should have received a copy of the GNU General Public License
   along with mTCP.  If not, see <http://www.gnu.org/licenses/>.


   Description: Bios Parameter Block data structure for DOS partitions.

   This isn't meant to fully model a BPB.  It's just enough for NetDrive
   along with some basic consistency checking.

*/

package bpb

import (
	"errors"
	"fmt"
)

// Type BPB is a minimal BIOS Parameter Block structure.
type BPB struct {
	BytesPerSector      uint16 // Byte per sector; always 512 in this implementation.
	SectorsPerCluster   uint8  // Must be a power of 2, max value is 64.
	ReservedSectorCount uint16 // Used for cluster alignment.
	NumFats             uint8  // Number of File Allocation Tables: 1 or 2
	RootDirEntries      uint16 // Number of root directory entries.  (Each is 32 bytes.)
	NumSectors          uint16 // Original number of sectors field.  (If 0 use HugeSectors.)
	MediaDescriptor     uint8  // A single byte that describes the media.
	SectorsPerFat       uint16 // Sectors used for each FAT copy. (Last field for DOS 2.x.)
	SectorsPerTrack     uint16 // Sectors per track. (Not used; introduced in DOS 3.0.)
	Heads               uint16 // Physical heads. (Not used.)
	HiddenSectors       uint32 // Before DOS 3.31 this is a uint16; DOS 3.31 and later use uint32
	HugeSectors         uint32 // Total logical sectors including hidden sectors.
}

// String converts a BPB to a human readable string for debug purposes.
func (b *BPB) String() string {
	return fmt.Sprintf("bps: %d, clsize: %d, RsvdSecs: %d, numFATs: %d, "+
		"RootDirSize: %d, TotalSecs: %d, MediaDesc: %02X, SecsPerFat: %d "+
		"SecsPerTrack: %d, Heads: %d, HiddenSecs: %d, HugeSecs: %d",
		b.BytesPerSector, b.SectorsPerCluster, b.ReservedSectorCount, b.NumFats,
		b.RootDirEntries, b.NumSectors, b.MediaDescriptor, b.SectorsPerFat,
		b.SectorsPerTrack, b.Heads, b.HiddenSectors, b.HugeSectors)
}

// TotalSectors returns the number of sectors in the image.
func (b *BPB) TotalSectors() int {
	if b.NumSectors != 0 {
		return int(b.NumSectors)
	} else {
		return int(b.HugeSectors)
	}
}

// New parses bytes, does some minimal error checking, and returns a BPB.
func New(b []byte) (*BPB, error) {

	if len(b) < 25 {
		return nil, errors.New("BPB input too short.")
	}

	bpb := new(BPB)

	bpb.BytesPerSector = uint16(b[0]) + (uint16(b[1]) * 256)
	bpb.SectorsPerCluster = b[2]
	bpb.ReservedSectorCount = uint16(b[3]) + (uint16(b[4]) * 256)
	bpb.NumFats = b[5]
	bpb.RootDirEntries = uint16(b[6]) + (uint16(b[7]) * 256)
	bpb.NumSectors = uint16(b[8]) + (uint16(b[9]) * 256)
	bpb.MediaDescriptor = b[10]
	bpb.SectorsPerFat = uint16(b[11]) + (uint16(b[12]) * 256)
	bpb.SectorsPerTrack = uint16(b[13]) + (uint16(b[14]) * 256)
	bpb.Heads = uint16(b[15]) + (uint16(b[16]) * 256)
	bpb.HiddenSectors = uint32(b[17]) + (uint32(b[18]) * 256) + (uint32(b[19]) * 65536) + (uint32(b[20]) * 16777216)
	bpb.HugeSectors = uint32(b[21]) + (uint32(b[22]) * 256) + (uint32(b[23]) * 65536) + (uint32(b[24]) * 16777216)

	// Flag things that are obviously wrong

	if bpb.BytesPerSector != 512 {
		return nil, errors.New("Bytes per sector is not 512")
	}

	if bpb.SectorsPerCluster == 0 {
		return nil, errors.New("SectorsPerCluster can not be 0")
	}

	if bpb.ReservedSectorCount == 0 {
		return nil, errors.New("ReservedSectorCount can not be 0")
	}

	if bpb.NumFats < 1 || bpb.NumFats > 2 {
		return nil, errors.New("NumFats must be 1 or 2")
	}

	if bpb.RootDirEntries == 0 {
		return nil, errors.New("RootDirEntries can not be 0")
	}

	if bpb.SectorsPerFat == 0 {
		return nil, errors.New("SectorsPerFat can not be 0")
	}

	// If the 16 bit NumSectors = 0 then HUgeSectors has to be set.  But don't
	// insist on HugeSectors being 0 if NumSectors is not zero because older
	// versions of DOS didn't know about HugeSectors and we might have read garbage.
	if bpb.NumSectors == 0 && bpb.HugeSectors == 0 {
		return nil, errors.New("Error on NumSectors and HugeSectors")
	}

	return bpb, nil
}
