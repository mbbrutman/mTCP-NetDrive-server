/*

   mTCP NetDrive Server (create.go)
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


   Description: Code for creating FAT12, FAT16 and FAT16B disk images.

*/

// Package create implements the command line tool used to create
// floppy disk or hard drive partition images with a FAT filesystem.
// FAT12, FAT16 and FAT16B variants are supported.  Floppy disk
// images are always FAT12 and must be one of the standard floppy
// disk images sizes: 160, 180, 320, 360, 720, 1200, 1440, or 2880 KB.
// Hard drive images can be from 1 to 2047 MB in size.
package create

import (
	"brutman.com/brutman/netdrv/bpb"
	"bytes"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
)


// FAT
//
// Some of this changes depending on which DOS version you are using.  I'm
// documenting what I have found here, and then choosing safe code/settings
// for all versions of DOS.
//
// Cluster values
//
//   000 means the sector is unused and available to use.
//   001 is never used as it is reserved
//   FF6 used to be the last valid data sector in early versions of DOS.
//   FF7 means there is a bad sector in the cluster.
//   FF8 - FFF means this is the last cluster of a file.
//
//   If somebody were to accidentally read slot 0 (which should never happen)
//   they would pick up the media descriptor byte.  All of the media descriptor
//   bytes fall in the range of F8 to FF, and with the extra F or FF added to
//   them they look like end of chain values.
//
//   Sometime around DOS 3.x FF0 was added as a "end of chain" marker too,
//   because F0 is also a valid media descriptor byte.  So it's just basically
//   simpler to avoid FF0 to FFF entirely.  (FFF0 to FFFF for 16 bit variants.)
//
// Special table entries entries
//
//   FAT entry 000 has the media descriptor byte with an extra F or FF.
//   FAT entry 001 is reserved and always has FFF or FFFF in it.
//   FAT entry 002 starts the mapping of the data area.
//
//
// So to create a safe image for FAT12 limit your FAT to 4078 entries
// numbered 2 to 0xFEF.  For FAT16 limit your FAT entries to 65518
// entries numbered 2 to 0xFFEF.
//
// DOS will calculate the number of clusters.  If you have 4085 or less
// clusters on the image then you are FAT12.  Above that you are FAT16.
// I have found the code in multiple DOS versions.  This is truth.


type floppyTableRec struct {
	sizeKB    int
	jumpBytes [3]byte
	oemName   [8]byte
	bpb       bpb.BPB
	fatBytes  [4]byte
}

// This is our table of supported floppy disk formats and the necessary
// bytes needed for the boot sector.  The DOS 1.x images are strange in
// that they don't use the BIOS Parameter Block structure, as that was
// introduced in DOS 2.0.

var floppyTable []floppyTableRec = []floppyTableRec{
	{160, [3]byte{0xeb, 0x27, 0x90}, [8]byte{0x08, 0x00, 0x14, 0x00, 0x00, 0x00, 0x00, 0x00},
		bpb.BPB{0x0000, 0x00, 0x0000, 0x00, 0x0000, 0x0000, 0x00, 0x0000, 0x0000, 0x0000, 0x00, 0x00}, [4]byte{0xfe, 0xff, 0xff, 0x00}},
	{180, [3]byte{0xeb, 0x34, 0x90}, oemBytes,
		bpb.BPB{0x0200, 0x01, 0x0001, 0x02, 0x0040, 0x0168, 0xfc, 0x0002, 0x0009, 0x0001, 0x00, 0x00}, [4]byte{0xfc, 0xff, 0xff, 0x00}},
	{320, [3]byte{0xeb, 0x27, 0x90}, [8]byte{0x03, 0x01, 0x14, 0x00, 0x00, 0x00, 0x00, 0x00},
		bpb.BPB{0x0000, 0x00, 0x0000, 0x00, 0x0000, 0x0000, 0x00, 0x0000, 0x0000, 0x0000, 0x00, 0x00}, [4]byte{0xff, 0xff, 0xff, 0x00}},
	{360, [3]byte{0xeb, 0x34, 0x90}, oemBytes,
		bpb.BPB{0x0200, 0x02, 0x0001, 0x02, 0x0070, 0x02d0, 0xfd, 0x0002, 0x0009, 0x0002, 0x00, 0x00}, [4]byte{0xfd, 0xff, 0xff, 0x00}},
	{720, [3]byte{0xeb, 0x3c, 0x90}, oemBytes,
		bpb.BPB{0x0200, 0x02, 0x0001, 0x02, 0x0070, 0x05a0, 0xf9, 0x0003, 0x0009, 0x0002, 0x00, 0x00}, [4]byte{0xf9, 0xff, 0xff, 0x00}},
	{1200, [3]byte{0xeb, 0x29, 0x90}, oemBytes,
		bpb.BPB{0x0200, 0x01, 0x0001, 0x02, 0x00e0, 0x0960, 0xf9, 0x0007, 0x000f, 0x0002, 0x00, 0x00}, [4]byte{0xf9, 0xff, 0xff, 0x00}},
	{1440, [3]byte{0xeb, 0x3c, 0x90}, oemBytes,
		bpb.BPB{0x0200, 0x01, 0x0001, 0x02, 0x00e0, 0x0b40, 0xf0, 0x0009, 0x0012, 0x0002, 0x00, 0x00}, [4]byte{0xf0, 0xff, 0xff, 0x00}},
	{2880, [3]byte{0xeb, 0x3c, 0x90}, oemBytes,
		bpb.BPB{0x0200, 0x02, 0x0001, 0x02, 0x00e0, 0x1680, 0xf0, 0x0009, 0x0024, 0x0002, 0x00, 0x00}, [4]byte{0xf0, 0xff, 0xff, 0x00}},
}

var oemBytes = [8]byte{'N', 'E', 'T', 'D', 'R', 'I', 'V', 'E'}
var hdJmpBytes = [3]byte{0xeb, 0x3c, 0x90}
var fat12FirstBytes = [4]byte{0xf8, 0xff, 0xff, 0x00}
var fat16FirstBytes = [4]byte{0xf8, 0xff, 0xff, 0xff}

func writeImage(fn string, sectors int, jumpBytes [3]byte, oemName [8]byte, bpb bpb.BPB, fatBytes [4]byte) error {

	if imageDir != "" {
		fn = imageDir + string(os.PathSeparator) + fn
	}

	// If the file exists do not overwrite it.
	_, err := os.Stat(fn)
	if err == nil {
		return errors.New("Output image file " + fn + " already exists.")
	}

	f, err := os.OpenFile(fn, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0666)
	if err != nil {
		return errors.New("Error opening image file (" + err.Error() + ")")
	}

	defer f.Close()

	// Write a file of zeros.  Try to do one megabyte at a time for efficiency,
	// then one last write for the remainder.

	sectorBuffer := make([]byte, 2048*512)

	for ; sectors >= 2048; sectors = sectors - 2048 {
		n, err := f.Write(sectorBuffer)
		if n != 2048*512 || err != nil {
			return errors.New("Error writing image file (" + err.Error() + ")")
		}
	}

	if sectors > 0 {
		remainder := sectorBuffer[0 : sectors*512]
		n, err := f.Write(remainder)
		if n != sectors*512 || err != nil {
			return errors.New("Error writing image file (" + err.Error() + ")")
		}
	}

	// Write the first few bytes of the boot sector.  Use bytes.Buffer so we
	// can specify little endian because some of these are words or double words.
	outbuf := new(bytes.Buffer)
	binary.Write(outbuf, binary.LittleEndian, jumpBytes)
	binary.Write(outbuf, binary.LittleEndian, oemName)
	binary.Write(outbuf, binary.LittleEndian, bpb)

	n, err := f.WriteAt(outbuf.Bytes(), 0)
	if n != 36 || err != nil {
		return errors.New("Error writing boot sector (" + err.Error() + ")")
	}

	firstFatSector := int64(bpb.ReservedSectorCount)
	n, err = f.WriteAt(fatBytes[:], firstFatSector*512)
	if n != 4 || err != nil {
		return errors.New("Error writing to first FAT (" + err.Error() + ")")
	}

	if bpb.NumFats == 2 {
		secondFatSector := int64(bpb.ReservedSectorCount) + int64(bpb.SectorsPerFat)
		n, err = f.WriteAt(fatBytes[:], secondFatSector*512)
		if n != 4 || err != nil {
			return errors.New("Error writing to second FAT (" + err.Error() + ")")
		}
	}

	return nil
}

func createFloppy(args []string) error {

	targetSize, err := strconv.Atoi(args[0])
	if err != nil {
		return errors.New("Invalid size specified for floppy.")
	}

	ti := -1
	for i := 0; i < len(floppyTable); i++ {
		if floppyTable[i].sizeKB == targetSize {
			ti = i
			break
		}
	}

	if ti == -1 {
		return errors.New("Invalid size specified for floppy.")
	}

	if (numFats != 2) || (int(numRootDirEntries) != 512) {
		fmt.Println("Ignoring -num_fats or -root_dir_entries options; this is a floppy.\n")
	}

	err = writeImage(args[1], targetSize*2, floppyTable[ti].jumpBytes,
		floppyTable[ti].oemName, floppyTable[ti].bpb, floppyTable[ti].fatBytes)
	if err != nil {
		return err
	}

	fmt.Printf("%v disk image created.\n", args[1])
	return nil
}


func calcSectorsPerFat(fatType string, clusters int) int {

	var sectorsPerFat int

	switch fatType {
	case "FAT12":
		bytesRequired := math.Ceil((float64(clusters) * 12.0) / 8.0)
		sectorsPerFat = int(math.Ceil(bytesRequired / 512.0))
	case "FAT16", "FAT16B":
		bytesRequired := clusters * 2
		sectorsPerFat = int(math.Ceil(float64(bytesRequired) / 512.0))
	}

	return sectorsPerFat
}



func createHd(args []string) error {

	if numFats != 1 && numFats != 2 {
		return errors.New("Invalid number of FAT copies specified.")
	}

	if numRootDirEntries < 16 || numRootDirEntries > 512 {
		return errors.New("numRootDirEntries should between 16 and 512 (inclusive).")
	}

	sizeMB, err := strconv.ParseFloat(args[0], 64)
	if err != nil {
		return errors.New("Unable to parse size.")
	}


	sectors := int(sizeMB * 1024 * 2)

	fatType := strings.ToUpper(args[1])

	switch fatType {
	case "FAT12", "FAT16":
		if sizeMB > 32 {
			return errors.New("FAT12 and FAT16 are limited to 32MB in size.")
		}
		// Fix a common off-by-one error when the user says 32MB.
		// 32MB is 65536 which we can't express in 16 bits.
		if sectors == 65536 {
			sectors = 65535
		}
	case "FAT16B":
		//if sizeMB > 2047.67 {
			//return errors.New("Sorry, the maximum hd image supported is 2047.67 MB.")
		//}
	default:
		return errors.New("You must choose either FAT12, FAT16, or FAT16B")
	}

	if fatType == "FAT16" || fatType == "FAT16B" {
		if sizeMB < 2 {
			return errors.New("FAT16 or FAT16B filesystems must be at least 2MB.")
		}
	}


	var minClusters int
	var maxClusters int
	var fatFirstBytes [4]byte

	switch fatType {
	case "FAT12":
		minClusters = 1
		maxClusters = 4078
		fatFirstBytes = fat12FirstBytes
	case "FAT16", "FAT16B":
		minClusters = 4086
		maxClusters = 65518
		fatFirstBytes = fat16FirstBytes
	}



	var bpb bpb.BPB

	bpb.BytesPerSector = 512
	bpb.ReservedSectorCount = 1
	bpb.NumFats = uint8(numFats)

	// If numRootDirEntries is not a multiple of 16 then round up to it because
	// it's going to use a full sector anyway.
	numRootDirEntries = (numRootDirEntries + 15) & 0xfff0

	bpb.RootDirEntries = uint16(numRootDirEntries)
	rootDirSectors := (int(bpb.RootDirEntries) * 32) / 512

	if sectors < 65536 {
		bpb.NumSectors = uint16(sectors)
		bpb.HugeSectors = 0
	} else {
		bpb.NumSectors = 0
		bpb.HugeSectors = uint32(sectors)
	}


	// Find a good sectors per cluster size.
	//
	// A small cluster size requires a larger FAT table but minimizes wasted
	// space across the filesystem.  Start with the minimum amount of wasted
	// space in the data area and increase the sectors per cluster until we
	// get a number of clusters that fits within our chosen FAT.


	// Calculate an initial number of data sectors and clusters using
	// a sectors per cluster of one.
	// 
	// We are guaranteed to be above the minimum number of clusters for both
	// FAT12 and FAT16/FAT16B because we enforced a minimum image size.
	// But we might also be above the maximum, so if we are over the maximum
	// keep doubling the sectors per cluster until we get under it.

	proposedClusterSize := 1

	if spc != 0 {
		proposedClusterSize = spc
	}

	dataSectors := sectors - int(bpb.ReservedSectorCount) - rootDirSectors
	clusters := dataSectors / proposedClusterSize

	sectorsPerFat := calcSectorsPerFat(fatType, clusters)

	dataSectors = dataSectors - (sectorsPerFat * int(bpb.NumFats))
	clusters = dataSectors / proposedClusterSize
	

	if spc != 0 {
		if (clusters < minClusters) || (clusters > maxClusters) {
			return fmt.Errorf( "Bad sectors_per_cluster: Computed clusters is out of range: (min: %v, yours: %v, max: %v)",
			minClusters, clusters, maxClusters)
		}
	} else {


		for {

			if proposedClusterSize > 64 {
				return errors.New("The specified disk is too big and requires sectors per cluster > 64.")
			}

			if clusters <= maxClusters {
				break
			}

			nextDataSectors := sectors - int(bpb.ReservedSectorCount) - rootDirSectors
			nextClusters := nextDataSectors / (proposedClusterSize * 2)

			nextSectorsPerFat := calcSectorsPerFat(fatType, nextClusters)

			nextDataSectors = nextDataSectors - (nextSectorsPerFat * int(bpb.NumFats))
			nextClusters = nextDataSectors / (proposedClusterSize * 2)

			if fatType != "FAT12" && nextClusters < 4086 {
				// This won't work because there are not enough clusters.
				break
			}

			proposedClusterSize = proposedClusterSize * 2

			dataSectors = nextDataSectors
			clusters = nextClusters
			sectorsPerFat = nextSectorsPerFat

		}

	}


	bpb.SectorsPerCluster = uint8(proposedClusterSize)
	bpb.MediaDescriptor = 0xF8
	bpb.SectorsPerFat = uint16(sectorsPerFat)
	bpb.SectorsPerTrack = 0
	bpb.Heads = 0
	bpb.HiddenSectors = 0

	fmt.Printf("Specified MB: %v, Filesystem: %v, Num FATs: %v, RootDirEntries: %v\n", sizeMB, fatType, bpb.NumFats, bpb.RootDirEntries)

	fmt.Printf("  Total sectors:    %7d\n", sectors)
	fmt.Printf("  Reserved sectors: %7d\n", bpb.ReservedSectorCount)
	fmt.Printf("  Root dir sectors: %7d\n", (bpb.RootDirEntries*32)/512)
	fmt.Printf("  FAT sectors:      %7d\n", bpb.SectorsPerFat * 2)
	fmt.Printf("  Data sectors:     %7d\n", dataSectors)
	fmt.Printf("  Clusters:         %7d, Cluster size: %v sectors (%vKB per cluster)\n", clusters, bpb.SectorsPerCluster, float64(bpb.SectorsPerCluster)/2.0)

	err = writeImage(args[2], sectors, hdJmpBytes, oemBytes, bpb, fatFirstBytes)
	if err != nil {
		return err
	}

	fmt.Printf("\n%v disk image created.\n", args[2])
	return nil
}

func createUsage() {
	fmt.Println(
		`
Format for the create command is:

  netdrive create floppy [command flags] <size> <image_filename>
or
  netdrive create hd [command flags] <size> <FAT12|FAT16|FAT16B> <image_filename>

Optional command flags:

  -image_dir <i>            Directory for images, default is the current dir
  -num_fats <n>             Hard drive image FAT copies (1 or 2), default 2
  -root_dir_entries <n>     Hard drive image root dir entries, default 512)
  -sectors_per_cluster <n>  Use n sectors per cluster (default is auto-compute)

Valid sizes for floppies are 160, 180, 320, 360, 720, 1200, 1440, 2880.
Hard drive sizes are in MB.
`)
}

// Use a specific error type to distinguish between usage errors and
// other errors.  Usage errors will get extra help text printed automatically.

type usageError struct {
	s string
}

func (u *usageError) Error() string {
	return u.s
}

var numFats int
var numRootDirEntries int
var imageDir string
var spc int

// Create creates a floppy or hard drive image.  It is designed to be called
// from a command line so the input parameter is a slice of command line
// arguments.  See the help text in the code for the required arguments.
func Create(args []string) (rc error) {

	defer func() {
		if rc != nil {
			fmt.Println("Error:", rc)
		}
		if _, ok := rc.(*usageError); ok == true {
			createUsage()
		}
	}()

	createFlags := flag.NewFlagSet("create flags", flag.ExitOnError)
	createFlags.IntVar(&numFats, "num_fats", 2, "Number of FAT copies to create")
	createFlags.IntVar(&numRootDirEntries, "root_dir_entries", 512, "Number of root directory entries")
	createFlags.IntVar(&spc, "sectors_per_cluster", 0, "Number of sectors per cluster")
	createFlags.StringVar(&imageDir, "image_dir", "", "Directory for disk images")
	createFlags.Usage = createUsage

	if len(args) < 1 {
		return &usageError{"Error: Not enough arguments for create"}
	}

	err := createFlags.Parse(args[1:])
	if err != nil {
		return &usageError{"Error parsing flags for create"}
	}

	switch strings.ToLower(args[0]) {
	case "help":
		createUsage()
		return nil
	case "floppy":
		if len(createFlags.Args()) < 2 {
			return &usageError{"You need to specify a size and an output image name."}
		}
		return createFloppy(createFlags.Args())
	case "hd":
		if len(createFlags.Args()) < 3 {
			return &usageError{"You need to specify a size, FAT type, and an output image name."}
		}
		return createHd(createFlags.Args())
	default:
		return &usageError{fmt.Sprintf("Unknown command: %s", args[0])}
	}

	return nil
}
