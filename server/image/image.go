/*

   mTCP NetDrive Server (image.go)
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


   Description: Code for reading and writing blocks in disk images.
   Includes all of the additional code for our implementation of
   journalling.

	 Image creation is in a different package.

*/

package image

import (
	"brutman.com/brutman/netdrv/globals"
	"brutman.com/brutman/netdrv/mlog"
	"bytes"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

// ImageFile represents an open image and its journal, if applicable.
// (Not all open images have a journal.)
type ImageFile struct {
	Filename    string
	Filesize    int64
	Readonly    bool
	JournalType JournalType
	Journal     *Journal_t
	file        *os.File
}

// String returns a human readable description of an ImageFile for debug purposes.
func (image *ImageFile) String() string {
	return fmt.Sprintf("Name: %s, Size: %d, Readonly: %v, Journal type: %v",
		image.Filename, image.Filesize, image.Readonly, image.JournalType.String())
}

// IsJournalOpen is used to determine if the ImageFile has an open journal.
func (image *ImageFile) IsJournalOpen() bool {
	if image == nil {
		return false
	}
	return image.Journal != nil
}

// OpenImage opens an image.  If the image has a persistent journal that will be opened too.
//
// If OpenImage is being called and and the image is marked as session scoped, a temporary
// session scoped journal will not be opened.  This is because we delay the creation of
// that until the user attempts to write the first block.
func OpenImage(fn string) (*ImageFile, error) {

	mlog.Debug("ImageFile:OpenImage: Attempting to open %v\n", fn)

	// Can we stat it and open it?  If so, add it to the open file map.

	fileInfo, err := os.Stat(fn)
	if err != nil {
		return nil, errors.New("image file not found: " + fn)
	}

	mlog.Debug("ImageFile:OpenImage: %v, bytes: %v, mode: %v\n", fn, fileInfo.Size(), fileInfo.Mode())

	if !fileInfo.Mode().IsRegular() {
		return nil, errors.New("image file is not a regular file: " + fn)
	}

	if (fileInfo.Size() % 512) != 0 {
		return nil, fmt.Errorf("specified image file size is not a multiple of 512 bytes: %s %d", fn, fileInfo.Size())
	}

	// Try to open it read-write first.  If that fails, fall back to read-only.

	var readonly bool = false

	f, err := os.OpenFile(fn, os.O_RDWR, 0)
	if err != nil {
		f, err = os.OpenFile(fn, os.O_RDONLY, 0)
		if err == nil {
			mlog.Info("ImageFile:OpenImage: %s is read-only\n", fn)
			readonly = true
		} else {
			mlog.Error("ImageFile:OpenImage: Error opening file %s: %s\n", fn, err)
			return nil, errors.New("error opening image file: " + fn)
		}
	}

	openImage := &ImageFile{
		Filename: fn,
		Filesize: fileInfo.Size(),
		Readonly: readonly,
		file:     f,
	}

	// Does this device have a GoBack style journal?  If so open it and read the map file.
	j, err := openGoBackJournal(fn)
	if err != nil {
		return nil, errors.New("error opening journal file for: " + fn)
	}

	if j != nil {
		openImage.Journal = j
		openImage.JournalType = GoBack
	}

	// Check to see if they want session scoped writes.
	sessionScoped := false
	tmpName := fn + ".session_scoped"
	if tmpF, err := os.OpenFile(tmpName, os.O_RDONLY, 0); err == nil {
		sessionScoped = true
		tmpF.Close()
	}

	// You can't have a device with both a journal and session scoped writes.
	//
	// If this does happen, we have already opened the journal and there is a session
	// start marker in it.  It's not worth the hassle to prevent that from happening.

	if sessionScoped == true {
		if j == nil {
			mlog.Debug("ImageFile:OpenImage: %s is enabled for session scoped writes\n", fn)
			openImage.JournalType = SessionScoped
		} else {
			mlog.Error("ImageFile:OpenImage: %s has a journal and is also marked for session scoped writes; there can only be one.\n", fn)
			openImage.Close()
			return nil, errors.New("unable to open journal file for: " + fn)
		}
	}

	mlog.Debug("ImageFile:OpenImage: New openImage created: %v\n", openImage)

	return openImage, nil
}

// Close cleans up an ImageFile.  This includes closing the underlying file descriptor
// and cleaning up an open journal if there is one.  (Session scoped
// journals are discarded after being closed.)
func (image *ImageFile) Close() error {

	errorsEncountered := false
	if err := image.file.Close(); err != nil {
		mlog.Error("ImageFile:Close: Error closing base image %s: %s\n", image.Filename, err)
		errorsEncountered = true
	}

	// Take care of the journal if there is one.
	if err := image.Journal.close(); err != nil {
		mlog.Error("ImageFile:Close: Error closing journal for %s: %s\n", image.Filename, err)
		errorsEncountered = true
	}

	if errorsEncountered {
		return errors.New("error closing image file or its journal")
	}

	return nil
}

// ReadLbas reads a number of blocks from an image and returns it in a byte slice.
// If a journal is open reads will come from the journal as needed.
func (image *ImageFile) ReadLbas(startLba globals.Lba, opCount uint16, sessionId int) ([]byte, error) {

	payload := make([]byte, 512*opCount)

	// Figure out if we are reading from the image file or its journal.

	journalRead := false

	if image.Journal != nil {
		for count := 0; count < int(opCount); count++ {
			if image.Journal.lbaPresent(startLba + globals.Lba(count)) {
				journalRead = true
				break
			}
		}
	}

	if journalRead == false {

		// Direct read into the image file.
		readOffset := int64(startLba) * 512
		n, err := image.file.ReadAt(payload, readOffset)
		if err != nil {
			mlog.Error("ImageFile:ReadLbas: Session %v Error reading image file at lba %d, offset %d, bytes needed %d: bytes read %d, err %s\n",
				sessionId, startLba, readOffset, 512*opCount, n, err)
			return nil, fmt.Errorf("read error from image at block %v", startLba)
		}

		mlog.Debug("ImageFile:ReadLbas: Session %v Read %d blocks at LBA %d from offset %d in image file\n", sessionId, opCount, startLba, readOffset)

	} else {

		// At least one sector is coming from the journal.  Read each sector individually.

		for count := 0; count < int(opCount); count++ {

			reqLba := startLba + globals.Lba(count)
			targetChunk := payload[(count * 512):((count + 1) * 512)]

			if image.Journal.lbaPresent(globals.Lba(reqLba)) {
				data, err := image.Journal.readLba(globals.Lba(reqLba))
				if err != nil {
					return nil, fmt.Errorf("read error from journal at block %v", reqLba)
				}
				copy(targetChunk, data)

			} else {

				readOffset := (int64(startLba) + int64(count)) * 512
				n, err := image.file.ReadAt(targetChunk, readOffset)
				if err != nil {
					mlog.Error("ImageFile:ReadLbas: Session %v Error reading image file at lba %d, offset %d, bytes needed %d: bytes read %d, err %s\n",
						sessionId, reqLba, readOffset, 512, n, err)
					return nil, fmt.Errorf("read error from image at block %v", reqLba)
				}
				mlog.Debug("ImageFile:ReadLbas: Session %v Read LBA %d from offset %d from image file\n", sessionId, reqLba, readOffset)

			}

		}
	}

	return payload, nil
}

// WriteLbas reads a number of blocks to an image or its journal, if applicable.
func (image *ImageFile) WriteLbas(startLba globals.Lba, opCount uint16, b []byte, sessionId int) error {

	if image.Journal == nil {
		n, err := image.file.WriteAt(b, int64(startLba)*512)
		if (err != nil) || (n != (512 * int(opCount))) {
			mlog.Error("ImageFile:WriteLbas: Session %v Error writing image file at lba %d, blocks %d, offset %d, err: %s\n",
				sessionId, startLba, opCount, int64(startLba)*512, err)
			return fmt.Errorf("write error in image at block %v", startLba)
		}
	} else {

		// If we are demoing this we don't want our guests to DoS us by using all
		// of our disk space.  Start throwing write errors after a set threshold
		// of unique blocks written

		// For simplicity only write to a session log file one sector at a time.
		for count := 0; count < int(opCount); count++ {
			reqLba := startLba + globals.Lba(count)
			chunk := b[(count * 512):((count + 1) * 512)]
			if err := image.Journal.writeLba(globals.Lba(reqLba), chunk); err != nil {
				return fmt.Errorf("journal write error at block %v", reqLba)
			}
		}

	}

	return nil
}

// JouralType describes the type of journal in use by ImageFile
type JournalType int

const (
	NoJournal     JournalType = iota // No journal in use.
	SessionScoped                    // This is a session scoped journal; it gets deleted when the session ends.
	GoBack                           // This is a persistent journal linked to an image.
)

// String converts JournalType to a human readable string.
func (w JournalType) String() string {
	return [...]string{"No Journal", "Session scoped", "GoBack"}[w]
}

// lbaMap_t is our in-memory data structure that maps an LBA to a record in a journal.
type lbaMap_t map[globals.Lba]uint32


const journalRecHdrSize = 16
const journalRecSize = journalRecHdrSize + 512

// journalRecHdr is our in-memory representation of a the header of a journal record.
// It is written using a binary encoding to ensure it is packed on disk.
//
// unsafe.Sizeof() and reflect.TypeOf(journalRecHdr{}).Size() will return 24 because
// the four byte fields are getting padded, not 16 as expected, so don't bother
// using those.
type journalRecHdr struct {
	Lba      globals.Lba
	Written  int64 // UnixMilli()
	Type     byte  // 0 for LBA, 1 for checkpoint mark
	Reserved [3]byte
}


const tag_maxlen = 64

// checkpointListRec is our in-memory list of checkpoints that we found in a journal
// when we opened it.
type checkpointListRec struct {
	recNum int
	ts     time.Time
	tag    string
}


// Journal_t is the metadata we need for a journal.
type Journal_t struct {
	journalType     JournalType         // SessionScoped or GoBack type journal
	lbaMap          lbaMap_t            // In-memory map of LBAs in the journal
	baseFilename    string              // Filename of the journaled device
	journalFile     *os.File            // Open journal file
	journalFilename string              // Open journal filename
	journalRecords  int                 // Number of records (approximate)
	checkpointList  []checkpointListRec // Possibly nil until the list is ready
}

// MapSize returns the number of LBAs resident in a journal.
func (j *Journal_t) MapSize() int {
	return len(j.lbaMap)
}

// lbaPresent 
func (j *Journal_t) lbaPresent(lba globals.Lba) bool {
	_, exists := j.lbaMap[lba]
	return exists
}

// JournalRecords returns the approximate number of records in the journal.
func (j *Journal_t) JournalRecords() int {
	return j.journalRecords
}

// OpenSessionJournal is used by the serving code to create a session scoped journal
// when it detects the first write is being made.  We don't just blindly open those
// journals when the session starts, and we use lazy evaluation instead.
func (image *ImageFile) OpenSessionJournal(bfn string, journalDir string) error {

	mlog.Debug("Journal:OpenSessionJournal: %s\n", bfn)

	jfn := bfn + ".log"

	if journalDir != "" {
		jfn = journalDir + string(os.PathSeparator) + jfn
	}

	f, err := os.OpenFile(jfn, os.O_RDWR|os.O_CREATE, 0644)

	if err != nil {
		mlog.Error("Journal:OpenSessionJournal: Error creating journal %s for %s: %s\n", jfn, bfn, err)
		return errors.New("error opening session journal")
	}

	journal := &Journal_t{SessionScoped, make(lbaMap_t, 128*1024), bfn, f, jfn, 0, nil}
	image.Journal = journal

	mlog.Info(fmt.Sprintf("Journal:OpenSessionJournal: Journal %s created for %s\n", jfn, bfn))

	return nil
}

// openJournalFile
//
// Open the journal file and ensure it is writeable.  This does the
// bare minimum and is designed to be used by higher level functions.
//
// Fixme: Consider allowing the journal to be readonly.  This would
// allow multiple concurrent connections.  For now if you wanted the
// equivalent function just copy the image and journal, commit the
// writes, and use that in read-only mode.
func openJournalFile(fn string) (*Journal_t, error) {

	mlog.Debug("Journal:openJournalFile: drive filename %s\n", fn)

	// Open read-only first to see if it exists.  Not existing is not an error.
	jfn := fn + ".journal"
	f, err := os.OpenFile(jfn, os.O_RDONLY, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			mlog.Info("Journal:openJournalFile: No journal found for %s\n", fn)
			return nil, nil
		}
		mlog.Error("Journal:openJournalFile: Error opening journal for %s: %s\n", fn, err)
		return nil, fmt.Errorf("error opening journal: %s, %w", fn, err)
	}

	// Now re-open it read-write.
	f.Close()
	f, err = os.OpenFile(jfn, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		mlog.Error("Journal:openJournalFile: Error opening journal %s in read-write mode: %s\n", jfn, err)
		return nil, fmt.Errorf("error opening journal: %s, %w", fn, err)
	}

	// Fixme: Do some basic checks on the journal to ensure it is usable without
	// resorting to scanning it.

	// Figure out the number of journal records
	fp, err := f.Seek(0, 2)
	if err != nil {
		mlog.Error("Journal:openJournalFile Error seeking to end of %s: %s\n", jfn, err)
		return nil, fmt.Errorf("error opening journal: %s, %w", fn, err)
	}
	records := fp / journalRecSize

	if (records * journalRecSize) != fp {
		mlog.Error("Journal:openJournalFile Journal file size is %d bytes, which is not a multiple of journalRecSize\n", fp)
		return nil, fmt.Errorf("journal file is not a valid size")
	}

	mlog.Info("Journal:openJournalFile: journal opened for %s, journal records: %d\n", fn, records)

	if records > 65536 {
		mlog.Warn("Journal:openJournalFile: Journal for %s is getting large, this might cause clients to time out: records %d\n", fn, records)
	}

	j := &Journal_t{GoBack, nil, fn, f, jfn, int(records), nil}
	return j, nil
}

// openGoBackJournal (GoBack mode only)
//
// If there is a GoBack style journal this will return it, otherwise it will
// return nil.   Returns a non-nil error if there is a journal but there was
// a problem reading it.  Also appends a session start checkpoint record and
// kicks off scanning the journal for checkpoint records in the background.
func openGoBackJournal(fn string) (*Journal_t, error) {

	mlog.Debug("Journal:openGoBackJournal: image filename %s\n", fn)

	// An error opening a journal is an error, but not having a journal is not an error.
	j, err := openJournalFile(fn)
	if err != nil {
		return nil, err
	}

	if j == nil {
		return nil, nil
	}

	// There should be a map file but it is not fatal if it is not there or
	// unreadable for some reason.  (Files that just got marked for journalling
	// the first time have no map file yet.)  However, if we resort to replaying
	// the journal and we can't then that should be a hard error.

	m, err := readMapfile(fn)
	if err != nil {
		m, err = j.replayJournal()
		if err != nil {
			mlog.Error("Journal:openGoBackJournal: No map file and journal replay failed for %s\n", fn)
			return nil, errors.New("error opening journal - replay failed")
		}
	} else {

		if strings.Contains(strings.ToUpper(globals.BuildDate), "DEBUG") {
			// Consistency checking code
			mlog.Info("Journal:openGoBackJournal: DEBUG version, running consistency check on journal map file.\n")
			m2, err := j.replayJournal()
			if err != nil {
				mlog.Error("Journal:openGoBackJournal: Replay failed during consistency check: %s %s\n", fn, err)
				return nil, errors.New("error opening journal - replay failed")
			}
			if areMapsEquivalent(m, m2) == false {
				mlog.Error("Journal:openGoBackJournal: Consistency failure: map doesn't match replay map\n")
				return nil, errors.New("error opening journal - consistency check failure")
			}
			mlog.Debug("Journal:openGoBackJournal: Map/journal consistency check passed.\n")
		}

	}

	j.lbaMap = m

	// Write the checkpoint mark for "Session Start" before building the
	// checkpoint list.  That way building the checkpoint list can be
	// kicked off in the background.  If that fails it will be detected
	// asynchronously, but it should not fail.

	_, err = j.writeCheckpointMark("Session Start")
	if err != nil {
		mlog.Error("Journal:openGoBackJournal: Error writing session start checkpoint mark in journal file %s: %s\n", fn, err)
		return nil, errors.New("error opening journal")
	}

	go j.createCheckpointList()

	return j, nil
}

// close
//
// This should aways be safe to call, even when a journal is not open.
// Session scoped journal files get deleted, while GoBack journals have
// their memory map written to disk.
func (j *Journal_t) close() error {

	mlog.Debug("Journal:close\n")

	// Ensure this is always safe to call.
	if j == nil {
		return nil
	}

	if err := j.journalFile.Close(); err != nil {
		mlog.Error("Journal:close: Error closing journal file %s: %s\n", j.journalFilename, err)
		return errors.New("error closing journal")
	}

	mlog.Info("Journal:close: journal file closed: %s\n", j.journalFilename)

	switch j.journalType {

	case SessionScoped:
		if err := os.Remove(j.journalFilename); err != nil {
			mlog.Error("Journal:close: Error deleting session journal file %s: %s\n", j.journalFilename, err)
			return errors.New("error closing journal")
		}
		mlog.Info("Journal:close: Deleted session journal %s\n", j.journalFilename)

	case GoBack:
		// Even though this should not happen, we're not going to treat it as
		// a hard error because the map can be recovered by a journal replay.
		// But we do log it as an error to investigate.
		if err := writeMapfile(j.journalFilename, j.lbaMap); err != nil {
			mlog.Error("Journal:close: error writing map for journal file %s\n", j.journalFilename)
		}
	}

	return nil
}

// readLba - Read an LBA from a journal.
//
// You need to know the LBA is in the journal before calling this, presumably by querying the journal map.
func (j *Journal_t) readLba(lba globals.Lba) ([]byte, error) {

	recNum := int(j.lbaMap[lba])

	b := make([]byte, journalRecSize)
	g, payload, err := j.readRecord(b, recNum)
	if err != nil {
		mlog.Error("Journal:readLba Read error reading lba %d from record %d in %s: %s\n", lba, recNum, j.journalFilename, err)
		return nil, errors.New("journal read error")
	}

	if g.Lba != lba {
		mlog.Error("Journal:readLba Tried to read lba %d from record %d in %s, got %d instead.\n",
			lba, recNum, j.journalFilename, g.Lba)
		return nil, errors.New("journal read error: LBA miscompare")
	}

	mlog.Debug("Journal:readLba Read lba %d from record %d in %s\n", lba, recNum, j.journalFilename)
	return payload, nil
}

// writeLba - Write an LBA to a journal.
func (j *Journal_t) writeLba(lba globals.Lba, userData []byte) error {

	recNum, exists := j.lbaMap[lba]

	// If we are using session scoped and this LBA is in the map, then overwrite
	// current location in the map file.  Otherwise, we are using GoBack or the
	// LBA has not been written so append to the journal.

	var fp int64
	if (exists == true) && (j.journalType == SessionScoped) {
		fp = int64(recNum * journalRecSize)
	} else {
		var err error
		fp, err = j.journalFile.Seek(0, 2)
		if err != nil {
			mlog.Error("Journal:writeLba Error seeking to end of %s while writing lba %d: %s\n", j.journalFilename, lba, err)
			return errors.New("journal write error")
		}
		recNum = uint32(fp / journalRecSize)

		// Assume the write will work.  If not it really doesn't matter, this is a rough statistic.
		j.journalRecords++
	}

	// Now both recNum and fp are set.

	g := journalRecHdr{
		Lba:     lba,
		Written: time.Now().UnixMilli(),
	}

	outbuf := new(bytes.Buffer)
	err := binary.Write(outbuf, binary.LittleEndian, g)
	outbuf.Write(userData)

	n, err := j.journalFile.WriteAt(outbuf.Bytes(), fp)
	if (err != nil) || (n != journalRecSize) {
		mlog.Error("Journal:writeLba Error writing lba %d to offset %d in %s: %s\n", lba, fp, j.journalFilename, err)
		return errors.New("journal write error")
	}

	// In session scoped writes this is not required if the LBA was already in the map, but it's not worth
	// doing two checks to determine that.
	j.lbaMap[lba] = recNum

	mlog.Debug("Journal:writeLba Wrote lba %d to offset %d in %s\n", lba, fp, j.journalFilename)

	return nil
}

// areMapsEquivalent
//
// This is a consistency checking function that is only used when debugging.
func areMapsEquivalent(m1 lbaMap_t, m2 lbaMap_t) bool {
	rc := true
	for k, v := range m1 {
		v2, exists := m2[k]
		if exists == false || (exists == true && v2 != v) {
			mlog.Debug("Miscompare! m1[%d]%d, m2: exists: %v\n", k, v, v2)
			rc = false
		}
	}
	for k, v := range m2 {
		v1, exists := m1[k]
		if exists == false || (exists == true && v1 != v) {
			mlog.Debug("Miscompare! m1[%d]%d, m2: exists: %v\n", k, v, v1)
			rc = false
		}
	}
	return rc
}

func readMapfile(fn string) (lbaMap_t, error) {

	mlog.Debug("Journal:readMapfile: drive filename %s\n", fn)

	start := time.Now()

	mfn := fn + ".journal.map"
	f, err := os.OpenFile(mfn, os.O_RDONLY, 0)
	if err != nil {
		mlog.Debug("Journal:readMapfile: Unable to open %s to read the LBA map: %s\n", mfn, err)
		return nil, errors.New("unable to open map file")
	}

	defer f.Close()

	writeMap := make(lbaMap_t, 32768) // 32K entries in the map is 16MB of unique LBA writes.
	tmpBuf := make([]byte, 8*1024)    // Process 8KB at a time from the file.

	// First read is the number of records in the file.  This is used to detect a truncated file.
	n, err := f.Read(tmpBuf[0:4])
	if err != nil {
		mlog.Error("Journal:readMapfile: failed to read the metadata, %d bytes read, err: %s\n", n, err)
		return nil, errors.New("read error on map file")
	}

	expectedRecords := int(tmpBuf[0]) + int(tmpBuf[1])<<8 + int(tmpBuf[2])<<16 + int(tmpBuf[3])<<24
	mlog.Debug("Journal:readMapfile: Expected records: %d\n", expectedRecords)

	for {

		// Read will return what is available, and if it's short that is not an error.
		n, err := f.Read(tmpBuf)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			mlog.Error("Journal:readMapfile: File error reading from map file %s: %s\n", mfn, err)
			return nil, errors.New("read error on map file")
		}

		// However, not getting two four byte integers worth of data is a problem.
		if (n % 8) != 0 {
			mlog.Error("Journal:readMapfile: Short read from map file %s: bytes: %d\n", mfn, n)
			return nil, errors.New("read error on map file")
		}

		for i := 0; i < n; i = i + 8 {
			lba := globals.Lba(tmpBuf[i]) + globals.Lba(tmpBuf[i+1])<<8 + globals.Lba(tmpBuf[i+2])<<16 + globals.Lba(tmpBuf[i+3])<<24
			rec := uint32(tmpBuf[i+4]) + uint32(tmpBuf[i+5])<<8 + uint32(tmpBuf[i+6])<<16 + uint32(tmpBuf[i+7])<<24
			writeMap[lba] = rec
		}

	}

	if len(writeMap) != expectedRecords {
		mlog.Error("Journal:readMapfile: Expected %d records but found %d\n", expectedRecords, len(writeMap))
		return nil, errors.New("map file wrong size")
	}

	elapsed := time.Since(start).Round(time.Millisecond)
	mlog.Debug("Journal:readMapfile: %d entries read from %s, elapsed time: %v\n", len(writeMap), mfn, elapsed)
	return writeMap, nil
}

// writeMapfile
//
// The map file is optional; we can always recover the write map by
// replaying the journal.  If for some reason we can't write the
// write map to the file completely and correctly, delete it so we
// force journal replay on the next open.
func writeMapfile(fn string, l lbaMap_t) (rc error) {

	rc = nil

	mlog.Debug("Journal:writeMapfile: Writing map file %s\n", fn)

	mfn := fn + ".map"
	f, err := os.OpenFile(mfn, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		mlog.Error("Journal:writeMapfile Unable to open %s to write the LBA map: %s\n", mfn, err)
		rc = errors.New("error writing map file")
		return
	}

	defer func() {
		f.Close()
		if rc != nil {
			mlog.Error("Journal:writeMapfile: Error writing map file %s: %s\n", mfn, rc)
			if err := os.Remove(mfn); err != nil {
				mlog.Error("Journal:writeMapfile: Unable to delete partially written mapfile: %s, %s:\n", mfn, err)
			} else {
				mlog.Debug("Journal:writeMapfile: Map file %s deleted\n", mfn)
			}
		}
	}()

	// The map might have shrunk in size compared to when we last wrote it.
	// Truncate the file so that it only has what we are going to write in this call.

	if err := f.Truncate(0); err != nil {
		mlog.Error("Journal:writeMapfile Unable to truncate the map file: %s %s\n", mfn, err)
		rc = errors.New("error writing map file")
		return
	}

	var tmpBuf [8 * 1024]byte

	// Write the length of the map in the first four bytes so we know what to expect on readback.
	tmpBuf[0] = byte(len(l))
	tmpBuf[1] = byte(len(l) >> 8)
	tmpBuf[2] = byte(len(l) >> 16)
	tmpBuf[3] = byte(len(l) >> 24)

	n, err := f.Write(tmpBuf[:4])
	if err != nil {
		mlog.Error("Journal:writeMapfile: Error writing expected records, bytes written: %d, err: %s\n", n, err)
		rc = errors.New("error writing map file")
		return
	}

	// The on-disk representation of the map is pretty simple - 4 byte key, 4 byte value.
	// Keep this simple by allocating an 8KB buffer, which is good for 1K pairs.

	tmpBufIndex := 0

	for k, v := range l {

		if tmpBufIndex == 8*1024 {
			n, err := f.Write(tmpBuf[:tmpBufIndex])
			if (n != tmpBufIndex) || (err != nil) {
				mlog.Error("Journal:writeMapfile: Short write on map file or error: %d bytes written, err: %s\n", n, err)
				rc = errors.New("error writing map file")
				return
			}
			tmpBufIndex = 0
		}

		tmpBuf[tmpBufIndex] = (byte)(k)
		tmpBuf[tmpBufIndex+1] = (byte)(k >> 8)
		tmpBuf[tmpBufIndex+2] = (byte)(k >> 16)
		tmpBuf[tmpBufIndex+3] = (byte)(k >> 24)
		tmpBuf[tmpBufIndex+4] = (byte)(v)
		tmpBuf[tmpBufIndex+5] = (byte)(v >> 8)
		tmpBuf[tmpBufIndex+6] = (byte)(v >> 16)
		tmpBuf[tmpBufIndex+7] = (byte)(v >> 24)

		tmpBufIndex = tmpBufIndex + 8

	}

	// Finish off the remainder of the buffer
	n, err = f.Write(tmpBuf[:tmpBufIndex])
	if (n != tmpBufIndex) || (err != nil) {
		mlog.Error("Journal:writeMapfile: Short write on map file or error: %d bytes written, err: %s\n", n, err)
		rc = errors.New("error writing map file")
		return
	}

	mlog.Info("Journal:writeMapfile: Wrote %d map entries for %s\n", len(l), fn)
	return
}

// readRecord
//
// This requires you to pass in a byte slice to use instead of creating one for you.
// This is for efficiency in code that reads a lot of records; it avoids churning
// the garbage collector by allowing the same buffer to be reused.
func (j *Journal_t) readRecord(b []byte, recNum int) (journalRecHdr, []byte, error) {

	var g journalRecHdr

	n, err := j.journalFile.ReadAt(b, int64(recNum*journalRecSize))

	// This means we read past the end of the journal which should only happen
	// if we are scanning it record by record, like on a replay.
	if err != nil {
		if errors.Is(err, io.EOF) {
			return g, b[journalRecHdrSize:], fmt.Errorf("Journal:readRecord %w", err)
		} else if errors.Is(err, os.ErrClosed) {
			mlog.Warn("Journal:readRecord tried to read from a closed journal file.\n")
			return g, b[journalRecHdrSize:], fmt.Errorf("Journal:readRecord %w", err)
		} else {
			mlog.Error("Journal:readRecord failed: recNum: %d, bytes read: %d, err: %s", recNum, n, err)
			return g, b[journalRecHdrSize:], fmt.Errorf("Journal:readRecord %w", err)
		}
	}

	// Parse the record header
	r := bytes.NewReader(b)
	err = binary.Read(r, binary.LittleEndian, &g)

	if err != nil {
		return g, b[journalRecHdrSize:], err
	}

	return g, b[journalRecHdrSize:], nil

}

// replayJournal - replay the journal to recreate the LBA map.
//
// Only called for devices that are journaled persistently, not session scoped journalling.
func (j *Journal_t) replayJournal() (lbaMap_t, error) {

	mlog.Debug("Journal:replayJournal: %s\n", j.journalFilename)

	start := time.Now()

	writeMap := make(lbaMap_t, 16*1024)
	b := make([]byte, journalRecSize)

	recNum := 0
	for ; ; recNum++ {

		g, _, err := j.readRecord(b, recNum)
		if errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			mlog.Error("Journal:replayJournal Error reading journal record, file: %s, record: %d, %s\n",
				j.journalFilename, recNum, err)
			return nil, fmt.Errorf("error reading journal record header.")
		}

		if g.Type == 0 {
			writeMap[g.Lba] = uint32(recNum)
		}

	}

	elapsed := time.Since(start).Round(time.Millisecond)

	mlog.Debug("Journal:replayJournal %s: LBA map entries: %d, File journal records %d, elapsed time: %v\n",
		j.journalFilename, len(writeMap), recNum, elapsed)

	return writeMap, nil
}

// createCheckpointList - scan the journal to find checkpoint markers.
//
// This can be called synchronously or launched as a go routine.  As a result
// it has a side effect of setting the checkpointList field in the journal
// if it completes, or just doing nothing if there is an error.
func (j *Journal_t) createCheckpointList() {

	if (j == nil) || (j.journalType != GoBack) {
		mlog.Info("Journal:createCheckpointList: not supported on this journal\n")
	}

	mlog.Debug("Journal:createCheckpointList\n")

	l := make([]checkpointListRec, 0, 100)

	b := make([]byte, journalRecSize)

	recNum := 0
	for ; ; recNum++ {

		g, payload, err := j.readRecord(b, recNum)

		// Eof is not an error.  But if we get a real error exit early without
		// creating the checkpoint list.  When somebody goes to look for it later
		// they'll get a warning about it not being available.

		if errors.Is(err, io.EOF) {
			break
		} else if errors.Is(err, os.ErrClosed) {
			mlog.Warn("Journal:createCheckpointList: Still building checkpoint list but file is closed, file: %s, record: %d, %s\n", j.journalFilename, recNum, err)
			return
		} else if err != nil {
			mlog.Error("Journal:createCheckpointList: Error reading journal record, file: %s, record: %d, %s\n", j.journalFilename, recNum, err)
			return
		}

		if g.Type == 1 {

			// The tag was written into a 512 byte slice of zeros, so it's always nul terminated.
			var tmpTag string
			nulIndex := bytes.IndexByte(payload[:], 0)
			if nulIndex != -1 {
				tmpTag = string(payload[:nulIndex])
			} else {
				mlog.Error("Journal:createCheckpointList: Error reading tag from journal file %s record %d\n", j.journalFilename, recNum)
			}

			mlog.Debug("Journal:createCheckpointList: Read checkpoint tag: %d %v %s\n", recNum, time.UnixMilli(g.Written), tmpTag)

			c := checkpointListRec{recNum, time.UnixMilli(g.Written), tmpTag}
			l = append(l, c)

		}

	}

	// If we made it this far we have a good list.  Otherwise, checkpointList
	// remains nil indicating that checkpoint functions depending on it are not
	// available.
	j.checkpointList = l
}

// GotoCheckpoint implements the undo feature for a journaled disk image.
//
// The function scans the checkpoint list backwards to find a matching
// checkpoint mark.  If there are new writes after that mark, truncate
// the file at the mark.  (A flag determines whether the mark is deleted
// or left in place.)
//
// This always provides a text message to the user.  In case of an error
// use the error as the message.
func (j *Journal_t) GotoCheckpoint(payload []byte) (string, error) {

	mlog.Debug("Journal:GotoCheckpoint: payload len: %v, payload bytes: %v\n", len(payload), payload)

	if (j == nil) || (j.journalType != GoBack) {
		mlog.Info("Journal:GotoCheckpoint: not supported on this journal\n")
		return "", errors.New("Journal: feature not supported")
	}

	// This should almost never happen - the background list building would have
	// to be very slow or fail.  Even if it does happen we just refuse to do this
	// work, which is not fatal.
	if j.checkpointList == nil {
		mlog.Info("Journal:GotoCheckpoint: checkpoint list not available yet.\n")
		return "", errors.New("Journal: not ready, please try again later")
	}

	if len(payload) < 4 {
		mlog.Debug("Journal:GotoCheckpoint: rejected, command payload too short\n")
		return "", errors.New("Bad request parameters")
	}

	reqType := payload[0]
	delMarker := payload[1]
	tagNum := int(payload[2]) + int(payload[3])*256

	// Basic consistency checking to ensure we didn't get a bad request.
	if (reqType > 1) || (delMarker > 1) ||              // Out of bounds
		((reqType == 0) && (len(payload) == 4)) ||        // Named tag but no tag name provided
		((reqType == 1) && (tagNum == 0)) ||              // Tag number but tag number is 0
		((reqType == 1) && (len(payload) != 4)) {         // Tag number and more than tag number provided
		mlog.Debug("Journal:GotoCheckpoint: rejected, bad request parameters\n")
		return "", errors.New("Bad request parameters")
	}

	var tagString string

	if reqType == 0 {
		tagString = string(payload[4:])
	}

	mlog.Debug("Journal:GotoCheckpoint: reqType: %d, delMarker: %d, tagNum: %d, tagString: %s\n",
		reqType, delMarker, tagNum, tagString)

	mlog.Debug("Journal:GotoCheckpoint: jfn: %s, journalrecords: %d, checkpointList size: %d\n",
		j.journalFilename, j.journalRecords, len(j.checkpointList))

	targetRecNum := -1
	targetListIndex := -1

	if reqType == 0 {

		// Do a backwards search of the checkpoint list to find the first matching
		// tag.  This is a sane way to handle duplicate tags; the newest ones are
		// the least destructive and probably what the user wants.

		for i := len(j.checkpointList) - 1; i >= 0; i-- {
			if j.checkpointList[i].tag == tagString {
				mlog.Debug("Journal:GotoCheckpoint: Tag found at element %d in checkpoint list: %v\n", i, j.checkpointList[i])
				targetRecNum = j.checkpointList[i].recNum
				targetListIndex = i
				break
			}
		}

		if targetRecNum == -1 {
			return "", fmt.Errorf("Checkpoint mark not found for tag %s", tagString)
		}

	} else {

		if tagNum < 1 || tagNum > len(j.checkpointList) {
			mlog.Debug("Journal:GotoCheckpoint: Bad tag number specified: %d\n", targetRecNum)
			return "", fmt.Errorf("Checkpoint number out of range: %d", tagNum)
		}

		targetRecNum = j.checkpointList[tagNum-1].recNum
		targetListIndex = tagNum - 1

	}

	mlog.Debug("Journal:GotoCheckpoint: targetRecNum: %d, targetListIndex: %d\n", targetRecNum, targetListIndex)

	// Seek to the end of file ourselves instead of using j.journalRecords.
	// It doesn't cost much and it provides a cross-check opportunity.
	fp, err := j.journalFile.Seek(0, 2)
	if err != nil {
		mlog.Error("Journal:GotoCheckpoint: Error seeking to the end of the journal: %s\n", err)
		// No damage done yet so this is not fatal.
		return "", errors.New("Journal failure")
	}

	if int(fp/journalRecSize) != j.journalRecords {
		mlog.Error("Journal:GotoCheckpoint: Consistency check, j.journalRecords is incorrect. fp: %d, records: %d\n",
			fp, j.journalRecords)
	}

	mlog.Debug("Journal:GotoCheckpoint: Current journal eof position: fp: %d, j.journalRecords: %d, targetRecNum: %d\n",
		fp, j.journalRecords, targetRecNum)

	// If the user said to delete the marker too then back up one more record.

	var userMsg string

	if delMarker == 1 {
		userMsg = fmt.Sprintf("Going to checkpoint (num: %d, tag %s) and deleting checkpoint marker",
			targetListIndex+1, j.checkpointList[targetListIndex].tag)
		targetRecNum--
		targetListIndex--
	} else {
		userMsg = fmt.Sprintf("Going to checkpoint (num: %d, tag %s) and leaving checkpoint marker",
			targetListIndex+1, j.checkpointList[targetListIndex].tag)
	}

	mlog.Debug("Journal:GotoCheckpoint: Truncating journal to %d\n", int64((targetRecNum+1)*journalRecSize))

	err = j.journalFile.Truncate(int64((targetRecNum + 1) * journalRecSize))
	if err != nil {
		mlog.Error("Journal:GotoCheckpoint: Error truncating journal: %s\n", err)
		return "", &globals.FatalError{errors.New("GotoCheckpoint failed")}
	}

	j.journalRecords = targetRecNum + 1

	mlog.Debug("Journal:GotoCheckpoint: New j.journalRecords: %d\n", j.journalRecords)

	mlog.Debug("Journal:GotoCheckpoint: Resizing list to %d\n", targetListIndex+1)
	j.checkpointList = j.checkpointList[0 : targetListIndex+1]

	if len(j.checkpointList) > 0 {
		mlog.Debug("Journal:GotoCheckpoint: New last element %d %v\n", len(j.checkpointList)-1, j.checkpointList[len(j.checkpointList)-1])
	} else {
		mlog.Debug("Journal:GotoCheckpoint: No elements in the checkpoint list.\n")
	}

	// Delete the old map file; it's not valid and it would cause corruption if
	// it were accidentally used.

	mfn := j.journalFilename + ".map"

	if _, err := os.Stat(mfn); err == nil {
		mlog.Info("Journal:GotoCheckpoint: Deleting old map file: %s\n", mfn)
		if err := os.Remove(mfn); err != nil {
			// Unable to delete the old map file?  That's fatal.
			mlog.Error("Journal:GotoCheckpoint: Error deleting map file %s: %s\n", mfn, err)
			return "", &globals.FatalError{fmt.Errorf("Error deleting map file %s; it must be deleted!", mfn)}
		}
	}

	n, err := j.replayJournal()
	if err != nil {
		mlog.Error("Journal:GotoCheckpoint: Error replaying truncated journal\n")
		return "", &globals.FatalError{errors.New("GotoCheckpoint failed")}
	}
	j.lbaMap = n

	// Might as well write it to disk.
	if err := writeMapfile(j.journalFilename, j.lbaMap); err != nil {
		mlog.Error("Journal:GotoCheckpoint: Error writing map file %s\n", j.journalFilename)
	}

	// return userMsg, &globals.FatalError{errors.New("Test error msg: GotoCheckpoint failed")}
	return userMsg, nil
}

// MarkCheckpoint writes a checkpoint into the journal.
func (j *Journal_t) MarkCheckpoint(tag string) error {

	mlog.Debug("Journal:MarkCheckpoint: tag len: %v, tag bytes: %v\n", len(tag), []byte(tag))

	if (j == nil) || (j.journalType != GoBack) {
		mlog.Info("Journal:MarkCheckpoint: not supported on this journal\n")
		return errors.New("Journal: feature not supported")
	}

	// This should almost never happen - the background list building would have
	// to be very slow or fail.
	if j.checkpointList == nil {
		mlog.Info("Journal:MarkCheckpoint: checkpoint list not available yet.\n")
		return errors.New("Journal: not ready, please try again later")
	}

	mlog.Debug("Journal:MarkCheckpoint: tag: %s\n", tag)

	t, err := j.writeCheckpointMark(tag)
	if err != nil {
		return err
	}

	j.checkpointList = append(j.checkpointList, t)
	return nil
}


var timestampFormat string = "2006-01-02 15:04:05.000"

// ListCheckpoints generates a list of checkpoints in the journal with the
// newest checkpoints appearing at the start of the list.
//
// Checkpoint numbers start at 1; 0 is reserved.
func (j *Journal_t) ListCheckpoints() ([]string, error) {

	var rc []string

	if (j == nil) || (j.journalType != GoBack) {
		mlog.Info("Journal:ListCheckpoints: not supported on this journal\n")
		return rc, errors.New("Journal: feature not supported")
	}

	// This should almost never happen - the background list building would have
	// to be very slow or fail.
	if j.checkpointList == nil {
		mlog.Info("Journal:ListCheckpoints: checkpoint list not available yet.\n")
		return rc, errors.New("Journal: not ready, please try again later")
	}

	rc = make([]string, 0, 20)

	for i := len(j.checkpointList) - 1; i >= 0; i-- {
		n := fmt.Sprintf("%5d %s %s",
			i+1, j.checkpointList[i].ts.Format(timestampFormat), j.checkpointList[i].tag)
		rc = append(rc, n)
	}

	return rc, nil
}

// writeCheckpointMark - private function that actually does the checkpoint mark write.
func (j *Journal_t) writeCheckpointMark(tag string) (checkpointListRec, error) {

	var r checkpointListRec

	if (len(tag) < 1) || (len(tag) > tag_maxlen) {
		mlog.Debug("Journal:writeCheckpointMark: Bad size for checkpoint tag: len %v\n", len(tag))
		return r, errors.New("Journal: Bad size for checkpoint tag")
	}

	fp, err := j.journalFile.Seek(0, 2)
	if err != nil {
		mlog.Error("Journal:writeCheckpointMark: Error seeking to the end of the journal: %s\n", err)
		return r, errors.New("Journal failure")
	}

	g := journalRecHdr{
		Lba:     0,
		Written: time.Now().UnixMilli(),
		Type:    1,
	}

	// Our byte slice is initialized to zeros so our tag is always terminated by a nul.
	outbuf := new(bytes.Buffer)
	err = binary.Write(outbuf, binary.LittleEndian, g)
	if err != nil {
		mlog.Error("Journal:writeCheckpointMark: Error encoding record: %s\n", err)
		return r, errors.New("Journal write error")
	}

	bogus := [512]byte{}
	copy(bogus[:], tag)
	outbuf.Write(bogus[:])

	if len(outbuf.Bytes()) != journalRecSize {
		mlog.Error("Journal:writeCheckpointMark: Record size is horribly wrong: bytes: %d\n", len(outbuf.Bytes()))
		return r, errors.New("Journal write error")
	}

	if _, err := j.journalFile.WriteAt(outbuf.Bytes(), fp); err != nil {
		mlog.Error("Journal:writeCheckpointMark: Error writing mark: %s\n", err)
		return r, errors.New("Journal write error")
	}

	j.journalRecords++

	r = checkpointListRec{int(fp / journalRecSize), time.UnixMilli(g.Written), tag}
	return r, nil
}

// CmdLine_ListCheckpoints is the command line utility to display the checkpoint
// markers in a journal file.  This is for offline use; it should not be run
// against images that are in use.  The input argument is an image file with an
// associated journal.
func CmdLine_ListCheckpoints(fn string) error {

	j, err := openJournalFile(fn)
	if err != nil {
		return fmt.Errorf("ListCheckpoints error: %s %s", fn, err)
	}

	if j == nil {
		return fmt.Errorf("%s does not have a journal file", fn)
	}

	defer j.journalFile.Close()

	j.createCheckpointList()

	if j.checkpointList == nil {
		return errors.New("Unable to scan journal for checkpoint markers.")
	}

	list, _ := j.ListCheckpoints()

	if len(list) > 0 {
		for _, v := range list {
			fmt.Println(v)
		}
	} else {
		fmt.Println("No checkpoint markers found")
	}

	return nil
}

// CmdLine_GotoCheckpoint implements the "goto checkpoint" function on a
// journaled image.  This is for offline use; attempting to use it on
// an active image file will result in corruption.
//
// See the help text for the arguments to pass in via the string slice.
func CmdLine_GotoCheckpoint(args []string) error {

	fn := args[0]

	var alphaTag string
	var markerNum int
	var delMarker bool

	switch strings.ToLower(args[1]) {
	case "-tag":
		alphaTag = args[2]
	case "-num":
		n, err := strconv.Atoi(args[2])
		if err != nil || n == 0 {
			return errors.New("Bad checkpoint number")
		}
		markerNum = n
	default:
		return errors.New("Must specify -tag or -num")
	}

	if len(args) == 4 {
		if strings.ToLower(args[3]) == "-del_marker" {
			delMarker = true
		} else {
			return errors.New("Bad flag or argument")
		}
	} else if len(args) > 4 {
		return errors.New("Bad flag or argument")
	}

	var p [4 + 64]byte
	pLen := 4

	if markerNum == 0 {
		copy(p[4:], alphaTag)
		pLen += len(alphaTag)
	} else {
		p[0] = 1
		p[2] = byte(markerNum & 0xff)
		p[3] = byte(markerNum >> 8)
	}

	if delMarker == true {
		p[1] = 1
	}

	j, err := openJournalFile(fn)
	if err != nil {
		return fmt.Errorf("Error opening journal file: %s %s", fn, err)
	}

	if j == nil {
		return errors.New("No journal file")
	}

	j.createCheckpointList()
	userMsg, err := j.GotoCheckpoint(p[0:pLen])

	if userMsg != "" {
		fmt.Println(userMsg)
	}

	j.close()

	return err
}

// rebuildMapFile - generates a new map file by replaying a journal.
//
// This is an offline function - the journal should not be in use elsewhere.
func rebuildMapFile(fn string) error {

	mfn := fn + ".journal.map"

	if _, err := os.Stat(mfn); err == nil {
		mlog.Info("Journal:rebuildMapFile: Deleting map file: %s\n", mfn)
		if err := os.Remove(mfn); err != nil {
			mlog.Error("Journal:rebuildMapFile: Error deleting map file %s: %s\n", mfn, err)
			return fmt.Errorf("Error deleting map file %s; please delete it before continuing", mfn)
		}
	}

	mlog.Info("Journal:rebuildMapFile: Opening journal to replay it and generate a new map file.")
	j, err := openJournalFile(fn)
	if err != nil {
		mlog.Error("Journal:rebuildMapFile: Opening %s to replay it: %s\n", fn, err)
		return fmt.Errorf("Error opening journal file to replay it %s", err)
	}

	m, err := j.replayJournal()
	if err != nil {
		mlog.Error("Journal:rebuildMapFile: Error replaying journal: %s\n", err)
		return fmt.Errorf("Error replaying journal: %s", err)
	}
	j.lbaMap = m

	// Now we can use the close method which takes care of the map file for us ...
	j.close()

	return nil
}

// ============================================================================

// Journal Compaction
//
// Compaction is made more complicated by checkpoint markers.  We can really only
// compact records between markers because if you don't respect those boundaries
// then you break the GoBack feature.
//
// * Open the journal file and create a blank map
// * Read journal entries building up the map.
// * When you hit a checkpoint mark
//   * Fseek back to where you started
//   * Copy records only if that record is in the map
//   * Do this up to hitting the next checkpoint mark, then erase the map and do it again.

// Compact is used to run a compaction process on a journal, eliminating blocks
// that effectively can't be seen.  When there are a lot of repeated writes to
// a small number of LBAs between checkpoint markers Compact can reclaim a lot of
// dead space.
//
// Due to the way checkpoint markers work, compaction only eliminates overwritten
// LBAs found between two checkpoints.  This is required to preserve the ability
// to go to a specific checkpoint.
func CmdLine_Compact(imageName string) error {

	mlog.Info("Journal:Compact: Called for image: %s\n", imageName)

	start := time.Now()

	// First check to see if there is a temp file from the last time compaction was
	// attempted.  If so something went wrong so do not proceed.

	journalFilename := imageName + ".journal"
	backupFilename := journalFilename + ".backup"

	// Use the more primitive form of open as we don't care about the existing
	// map file and we don't want to append a session start marker.

	j, err := openJournalFile(imageName)
	if err != nil {
		mlog.Error("Journal:Compact: Error opening journal file for: %s, %s\n", imageName, err)
		return fmt.Errorf("Error opening journal file: %s, %s", imageName, err)
	}

	if j == nil {
		return errors.New("No journal to compact")
	}

	mlog.Info("Journal:Compact: Journal %s opened for compaction\n", j.journalFilename)
	fmt.Printf("Opened %s for reading.\n", j.journalFilename)

	// Ensure this is closed even though we'll explicitly close it later.  This covers error cases.
	defer j.journalFile.Close()

	_, err = j.journalFile.Seek(0, 0)
	if err != nil {
		mlog.Error("Journal:Compact: Error seeking to the start of the journal file: %s, %s\n", j.journalFilename, err)
		return fmt.Errorf("Seek error: %s", err)
	}

	// Create a temp file to write the active journal records to.
	compactedFile, err := os.CreateTemp(".", "NetDrive*.compacted_journal")
	if err != nil {
		mlog.Error("Journal:Compact: Error opening temp file to write compacted journal to: %s\n", err)
		return fmt.Errorf("Error opening new file to write compacted records to: %s", err)
	}
	compactedFilename := compactedFile.Name()

	// Ensure this is closed even though we'll explicitly close it later.  This covers error cases.
	defer compactedFile.Close()

	mlog.Info("Journal:Compact: Opened temp file %s to write the compacted journal to.\n", compactedFilename)

	fmt.Printf("Opened temp file %s to write the compacted journal to.\n", compactedFilename)

	fmt.Print("Copying valid records to the new journal ")

	recordsRead := 0
	recordsWritten := 0
	checkpointRecordsFound := 0

	b := make([]byte, journalRecSize)
	recNum := 0

	for {

		// Phase 1: Scan ahead
		//
		// At the top of the loop note where we are.  We will scan ahead to the next
		// checkpoint record (or EOF) and then rewind back to this point to do the
		// actual record copying.

		recNumCycleStart := recNum

		// Create a temporary map that will hold the mapping between active LBAs and
		// record numbers during this pass.
		m := make(lbaMap_t, 16384)

		for ; ; recNum++ {

			if (recNum % 5000) == 0 {
				fmt.Print(".")
			}

			g, _, err := j.readRecord(b, recNum)
			if errors.Is(err, io.EOF) {
				break
			} else if err != nil {
				mlog.Error("Journal:Compact: Read pass: Error reading record %d: %s\n", recNum, err)
				return fmt.Errorf("Error reading record %d: %s", recNum, err)
			}

			mlog.Debug("Journal:Compact: Read pass: Record num %6d, Record type %d, LBA %8d\n", recNum, g.Type, g.Lba)

			recordsRead++

			if g.Type == 0 {
				m[g.Lba] = uint32(recNum)
			} else if g.Type == 1 {
				checkpointRecordsFound++
				break
			}

		}

		// Phase two: Reread and copy records if they are referenced by the map or they are
		// checkpoint records.
		//
		// We scan sequentially instead of just iterating over the map because from an I/O
		// perspective the sequential scan is probably faster.

		recNum = recNumCycleStart
		eof := false

		for ; ; recNum++ {

			g, _, err := j.readRecord(b, recNum)
			if errors.Is(err, io.EOF) {
				eof = true
				break
			} else if err != nil {
				mlog.Error("Journal:Compact: Write pass: Error reading record %d: %s\n", recNum, err)
				return fmt.Errorf("Error reading record %d: %s", recNum, err)
			}

			mlog.Debug("Journal:Compact: Write pass: Record num %6d, Record type %d, LBA %8d\n", recNum, g.Type, g.Lba)

			if ((g.Type == 0) && (m[g.Lba] == uint32(recNum))) || (g.Type == 1) {
				n, err := compactedFile.Write(b)
				if err != nil {
					mlog.Error("Journal:Compact: Write pass: Write error, %d bytes written: %s\n", n, err)
					return fmt.Errorf("Write error: %s", err)
				}
				recordsWritten++
			}

			if g.Type == 1 {
				// Stop here and start the next read pass
				break
			}

		}

		if eof == true {
			break
		}

		recNum++

	}

	fmt.Println(" Done!")

	// Even though we used defer to do this we have to explicitly close these
	// here, otherwise the next few file operations will fail.
	//
	// Don't use the journal close method because it writes out the map data
	// structure.  We're need to replay on the new journal to generate that.

	j.journalFile.Close()
	compactedFile.Close()

	fmt.Printf("Done: %d records scanned, %d active records written, %d checkpoint records found.\n",
		recordsRead, recordsWritten, checkpointRecordsFound)

	// Rename the current journal for safety; if something fails it can be
	// renamed and used again by the user.
	mlog.Info("Journal:Compact: Renaming current journal: %s -> %s\n", journalFilename, backupFilename)
	fmt.Printf("Renaming current journal from %s to %s.\n", journalFilename, backupFilename)
	if err := os.Rename(journalFilename, backupFilename); err != nil {
		mlog.Error("Journal:Compact: Error renaming %s to %s: %s\n", journalFilename, backupFilename, err)
		return fmt.Errorf("Error: unable to move current journal (%s) to %s; is there an old one there?", journalFilename, backupFilename)
	}

	// Now the new journal replaces the old journal.
	mlog.Info("Journal:Compact: Renaming compacted journal: %s -> %s\n", compactedFilename, journalFilename)
	fmt.Printf("Renaming new compacted journal to %s.\n", journalFilename)
	if err := os.Rename(compactedFilename, journalFilename); err != nil {
		mlog.Error("Journal:Compact: Error moving %s to %s: %s\n", compactedFilename, journalFilename, err)
		return fmt.Errorf("Error: unable to rename %s to %s; leaving old journal in %s", compactedFilename, journalFilename, backupFilename)
	}

	fmt.Println("Rebuilding map file for journal.")
	if err := rebuildMapFile(imageName); err != nil {
		mlog.Error("Journal:Compact: Error rebuilding map file: %s\n")
		return err
	}

	// The new journal is in place and it can be replayed so all is good.  Leave
	// the backup in place just in case.

	mlog.Info("Journal:Compact: All done, old journal left in place at %s\n", backupFilename)
	fmt.Printf("\nDone - Please delete the backup file (%s) after testing the image.\n", backupFilename)

	elapsed := time.Since(start).Round(time.Millisecond)

	mlog.Info("Journal:Compact: Elapsed time: %v\n", elapsed)
	fmt.Printf("Elapsed time: %v\n", elapsed)

	return nil
}

// Commit
//
// Given a disk image and a checkpoint number, merge the records up to the
// checkpoint mark into the disk image.  For debug purposes up front we'll
// copy the disk image to a temporary file and merge into that instead.
//
// Rough process
//
// * Open the file
// * Open the journal
// * Copy the file to a temp file
// * Copy the journal records into the temp file
// * Copy the remaining journal records to a new journal file
// * Replay that journal so we can write a new map file

// Commit is used to commit journal entries into an image file.  You can
// choose to commit all of the journal or only a part of it.   A partial
// commit requires you to specify a checkpoint marker.  Writes
// made before that marker will be committed, and writes made after that
// marker will remain in the journal.
//
// See the help text for the arguments to pass in via the string slice.
func CmdLine_Commit(imageName string, checkpointNum int) error {

	mlog.Info("Journal:Commit: Called for image: %s\n", imageName)

	start := time.Now()

	fmt.Printf("Opening image file %s in read-only mode.\n", imageName)
	sourceImageFile, err := os.OpenFile(imageName, os.O_RDONLY, 0)
	if err != nil {
		return fmt.Errorf("Error opening image file: %s", err)
	}
	defer sourceImageFile.Close()

	fmt.Println("Opening journal file for image file.")
	j, err := openJournalFile(imageName)
	if err != nil {
		return fmt.Errorf("Error opening journal file: %s", err)
	}

	fmt.Println("Finding checkpoint markers in the journal file.")
	j.createCheckpointList()
	if j.checkpointList == nil {
		return fmt.Errorf("Unable to create checkpoint list")
	}

	if checkpointNum > 0 && checkpointNum > len(j.checkpointList) {
		return fmt.Errorf("Checkpoint num needs to be between 1 and %d, or all to commit everything.", len(j.checkpointList))
	}

	if checkpointNum == 0 {
		fmt.Println("Commiting all journal entries to the base image.")
	} else {
		fmt.Printf("Commiting journal writes up to checkpoint mark: %d, %v, %s\n",
			checkpointNum,
			j.checkpointList[checkpointNum-1].ts.Format(timestampFormat),
			j.checkpointList[checkpointNum-1].tag)
	}

	fmt.Println("Copying image file to a temp image file.")
	targetImageFile, err := os.CreateTemp(".", "NetDrive*.image")
	if err != nil {
		return fmt.Errorf("Error creating temp file to copy image to")
	}
	targetImageFilename := targetImageFile.Name()
	defer targetImageFile.Close()

	if _, err := io.Copy(targetImageFile, sourceImageFile); err != nil {
		return fmt.Errorf("Error copying source image file: %s", err)
	}

	sourceImageFile.Close()

	fmt.Print("Commiting journal records to the temp image file ")

	b := make([]byte, journalRecSize)

	eof := false
	recNum := 0
	for ; ; recNum++ {

		if (recNum % 5000) == 0 {
			fmt.Print(".")
		}

		g, payload, err := j.readRecord(b, recNum)
		if errors.Is(err, io.EOF) {
			eof = true
			break
		} else if err != nil {
			return fmt.Errorf("Error reading journal record: %s", err)
		}

		// If we are merging to a specific checkpoint number see if it is time to stop.
		if checkpointNum != 0 && g.Type == 1 {
			if recNum == j.checkpointList[checkpointNum-1].recNum {
				break
			}
		}

		if g.Type == 0 {
			_, err := targetImageFile.WriteAt(payload, int64(g.Lba)*512)
			if err != nil {
				return fmt.Errorf("Error writing record %d (LBA %d) into image: %s", recNum, g.Lba, err)
			}
		}

	}
	fmt.Println(" Done!")

	targetImageFile.Close()

	if eof == true {
		fmt.Print("Entire journal processed.")
	} else {
		fmt.Print("Copying remaining journal records to a new journal file")
	}

	// Create a new file for the remaining journal records even if it is
	// a zero length file.

	b = make([]byte, 1024*1024)

	newJournalFilename := targetImageFilename + ".journal"
	newJournal, err := os.Create(newJournalFilename)

	defer newJournal.Close()

	// Position the input file to the checkpoint record where we stopped
	_, err = j.journalFile.Seek(int64(recNum)*journalRecSize, 0)
	if err != nil {
		return fmt.Errorf("Seek error: %s", err)
	}

	for {
		n, err := j.journalFile.Read(b)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			} else {
				return fmt.Errorf("Read error copying remaining journal records: %s", err)
			}
		}
		fmt.Print(".")
		_, err = newJournal.Write(b[0:n])
		if err != nil {
			return fmt.Errorf("Write error copying remaining journal records: %s", err)
		}
	}
	fmt.Println(" Done!")

	j.journalFile.Close()
	newJournal.Close()

	// Rename files

	saveName := "BACKUP_" + imageName
	fmt.Printf("Renaming original image file %s to %s.\n", imageName, saveName)
	mlog.Info("Journal:Commit: Renaming image file: %s -> %s\n", imageName, saveName)
	if err := os.Rename(imageName, saveName); err != nil {
		mlog.Error("Journal:Commit: Error renaming %s to %s: %s\n", imageName, saveName, err)
		return fmt.Errorf("Error: unable to rename image file (%s) to (%s)", imageName, saveName)
	}

	saveJournalName := "BACKUP_" + j.journalFilename
	fmt.Printf("Renaming original journal file %s to %s.\n", j.journalFilename, saveJournalName)
	mlog.Info("Journal:Commit: Renaming journal file: %s -> %s\n", j.journalFilename, saveJournalName)
	if err := os.Rename(j.journalFilename, saveJournalName); err != nil {
		mlog.Error("Journal:Commit: Error renaming %s to %s: %s\n", j.journalFilename, saveJournalName, err)
		return fmt.Errorf("Error: unable to rename image file (%s) to (%s)", j.journalFilename, saveJournalName)
	}

	fmt.Printf("Renaming temp files to %s and %s.\n", imageName, j.journalFilename)

	mlog.Info("Journal:Commit: Renaming temp image file: %s -> %s\n", targetImageFilename, imageName)
	if err := os.Rename(targetImageFilename, imageName); err != nil {
		mlog.Error("Journal:Commit: Error renaming %s to %s: %s\n", targetImageFilename, imageName, err)
		return fmt.Errorf("Error: unable to rename new image file (%s) to (%s)", targetImageFilename, imageName)
	}

	mlog.Info("Journal:Commit: Renaming temp journal file: %s -> %s\n", newJournalFilename, j.journalFilename)
	if err := os.Rename(newJournalFilename, j.journalFilename); err != nil {
		mlog.Error("Journal:Commit: Error renaming %s to %s: %s\n", newJournalFilename, j.journalFilename, err)
		return fmt.Errorf("Error: unable to rename new image file (%s) to (%s)", newJournalFilename, j.journalFilename)
	}

	fmt.Println("Rebuilding map file.")
	if err := rebuildMapFile(imageName); err != nil {
		mlog.Error("Journal:Commit: Error rebuilding map file: %s\n")
		return err
	}

	fmt.Printf("\nDone - Please delete the backup files (%s and %s) after testing the image.\n", saveName, saveJournalName)

	elapsed := time.Since(start).Round(time.Millisecond)

	mlog.Info("Journal:Commit: Elapsed time: %v\n", elapsed)
	fmt.Printf("Elapsed time: %v\n", elapsed)

	return nil
}

// Compare will compare two images and tell you if they are equivalent to each
// other.   If an image has a journal it will be opened and used during the
// comparison phase.
//
// Compare is useful for ensuring that a Commit or Compact operation worked
// correctly.  First create a copy of the original image and its journal,
// then run the Commit or Compact operation.  Compare can then be used against
// the backup copy and the current copy to validate the operation was
// performed correctly.
func CmdLine_Compare(target1 string, target2 string) error {

	start := time.Now()

	f1, err := os.OpenFile(target1, os.O_RDONLY, 0)
	if err != nil {
		return fmt.Errorf("Error opening image file: %s", err)
	}

	defer f1.Close()

	j1, err := openJournalFile(target1)
	if err != nil {
		return fmt.Errorf("Error opening journal for %s", target1)
	}
	if j1 != nil {
		m, err := readMapfile(target1)
		if err != nil {
			m, err = j1.replayJournal()
			if err != nil {
				return fmt.Errorf("Error opening journal for %s", target1)
			}
		}
		j1.lbaMap = m
	}

	f2, err := os.OpenFile(target2, os.O_RDONLY, 0)
	if err != nil {
		return fmt.Errorf("Error opening image file: %s", err)
	}

	defer f2.Close()

	j2, err := openJournalFile(target2)
	if err != nil {
		return fmt.Errorf("Error opening journal for %s", target2)
	}
	if j2 != nil {
		m, err := readMapfile(target2)
		if err != nil {
			m, err = j2.replayJournal()
			if err != nil {
				return fmt.Errorf("Error opening journal for %s", target2)
			}
		}
		j2.lbaMap = m
	}

	fmt.Printf("Image 1: %v, Journaled: %v", target1, (j1 != nil))
	if j1 != nil {
		fmt.Printf(", Journal records: %v\n", j1.journalRecords)
	} else {
		fmt.Println("")
	}
	fmt.Printf("Image 2: %v, Journaled: %v", target2, (j2 != nil))
	if j2 != nil {
		fmt.Printf(", Journal records: %v\n", j2.journalRecords)
	} else {
		fmt.Println("")
	}

	fmt.Println("")

	buf1 := make([]byte, journalRecSize)
	buf2 := make([]byte, journalRecSize)

	payload1 := buf1[journalRecHdrSize:]
	payload2 := buf2[journalRecHdrSize:]

	differences := 0

	done := false
	lba := 0
	for ; !done; lba++ {

		if j1 == nil || j1.lbaPresent(globals.Lba(lba)) == false {
			_, err := f1.ReadAt(payload1, int64(lba*512))
			if err != nil {
				if errors.Is(err, io.EOF) {
					done = true
					break
				}
				return fmt.Errorf("Error reading block %d from %s: %s", lba, target1, err)
			}
		} else {
			g, _, err := j1.readRecord(buf1, int(j1.lbaMap[globals.Lba(lba)]))
			if err != nil {
				return fmt.Errorf("Error reading lba %d from journal for %s: %s", lba, target1, err)
			}
			if int(g.Lba) != lba {
				return fmt.Errorf("LBA miscompare with journal record, expected %d, got %d", lba, g.Lba)
			}
		}

		if j2 == nil || j2.lbaPresent(globals.Lba(lba)) == false {
			_, err := f2.ReadAt(payload2, int64(lba*512))
			if err != nil {
				if errors.Is(err, io.EOF) {
					done = true
					break
				}
				return fmt.Errorf("Error reading block %d from %s: %s", lba, target2, err)
			}
		} else {
			g, _, err := j2.readRecord(buf2, int(j2.lbaMap[globals.Lba(lba)]))
			if err != nil {
				return fmt.Errorf("Error reading lba %d from journal for %s: %s", lba, target2, err)
			}
			if int(g.Lba) != lba {
				return fmt.Errorf("LBA miscompare with journal record, expected %d, got %d", lba, g.Lba)
			}
		}

		if !bytes.Equal(payload1, payload2) {
			differences++
			if differences < 10 {
				fmt.Printf("Different data found at LBA %d\n", lba)
			} else if differences == 10 {
				fmt.Println("More than 10 different LBAs found, suppressing messages.")
			}
		}

	}

	elapsed := time.Since(start).Round(time.Millisecond)

	if differences == 0 {
		fmt.Printf("%s and %s are equivalent\n", target1, target2)
	} else {
		fmt.Printf("\nDifferent blocks: %d\n", differences)
	}

	fmt.Printf("%d LBAs compared, Elapsed time: %v\n", lba, elapsed)

	return nil
}

func imageCmdLineUsage() {
	fmt.Println(
		`
Image commands:

  compact_journal <drive_image>

    compact_journal is used to remove invalided journal records, possibly
    making the journal file smaller.

  list_cp <drive_image>

    list_cp shows the checkpoint markers in an image.

  goto_cp <drive_image> -tag <tag> [-del_marker]
  goto_cp <drive_image> -num <checkpoint_marker_num> [-del_marker]

    Undo the newest writes in a journal by deleting all of the writes
    past a checkpoint marker.  The checkpoint marker can be specified
    by an alphanumeric tag or by a number shown by list_cp.  By default
    the checkpoint marker is left in place; use the -del_marker option
    to also remove the checkpoint marker.

  commit_writes <drive_image> <all|checkpoint number>

    commit_writes is used to make the writes recorded in a journal permanent
    in the image.  You can choose to commit all writes in the journal, or
    specify a checkpoint number to commit up to.  The base image will be
    modified and the journal will shrink in size to reflect the writes that
    have been commited.

  compare <drive_image_1> <drive_image_2>

    compare_images is used to compare two images for equivalence, meaning
    every logical sector of the images contains the same bytes.  (Images
    with journals are supported and can be compared to images without
    journals.)  
`)
}

// Use a specific error type to distinguish between usage errors and
// other errors.  Usage errors will get extra text printed automatically.

type usageError struct {
	s string
}

func (u *usageError) Error() string {
	return u.s
}

// CmdLine is the command line processor for the offline functions that
// perform image and journal maintenance.
//
// See the help text for the arguments to pass in via the string slice.
func CmdLine(args []string) (rc error) {

	defer func() {
		if rc != nil {
			fmt.Println("Error:", rc)
		}
		if _, ok := rc.(*usageError); ok == true {
			imageCmdLineUsage()
		}
	}()

	imageCmdLineFlags := flag.NewFlagSet("Image flags", flag.ExitOnError)
	imageCmdLineFlags.Usage = imageCmdLineUsage

	if len(args) < 1 {
		return &usageError{"Not enough arguments for image"}
	}

	err := imageCmdLineFlags.Parse(args[1:])
	if err != nil {
		return &usageError{"Error parsing flags for image"}
	}

	switch strings.ToLower(args[0]) {
	case "help":
		imageCmdLineUsage()
		return nil
	case "compact_journal":
		if len(args) < 2 {
			return &usageError{"Specify an image name"}
		}
		return CmdLine_Compact(args[1])
	case "list_cp":
		if len(args) < 2 {
			return &usageError{"Specify an image name."}
		}
		return CmdLine_ListCheckpoints(args[1])
	case "goto_cp":
		if len(args) < 4 {
			return &usageError{"Not enough arguments"}
		}
		return CmdLine_GotoCheckpoint(args[1:])
	case "commit_writes":
		if len(args) < 3 {
			return &usageError{"Specify an image name and a checkpoint number."}
		}
		checkpointNum := -1
		if strings.ToLower(args[2]) == "all" {
			checkpointNum = 0
		} else {
			checkpointNum, err = strconv.Atoi(args[2])
			if err != nil {
				return &usageError{"Bad checkpoint number, use \"all\" or a numeric checkpoint number."}
			}
		}
		return CmdLine_Commit(args[1], checkpointNum)
	case "compare":
		if len(args) < 3 {
			return &usageError{"Specify two images to compare."}
		}
		return CmdLine_Compare(args[1], args[2])
	default:
		return &usageError{fmt.Sprintf("Unknown command: %s", args[0])}
	}

	return nil
}
