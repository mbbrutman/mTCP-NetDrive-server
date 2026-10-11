/*

   mTCP NetDrive Server (server.go)
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


   Description: The NetDrive remote disk image server.

*/

// Package serve implements the NetDrive server functions.  The three core
// pieces of function include session management, running commands on behalf
// of connected clients, and providing a text-mode user interface.
package serve

import (
	"bytes"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"brutman.com/brutman/netdrive/bpb"
	"brutman.com/brutman/netdrive/globals"
	"brutman.com/brutman/netdrive/image"
	"brutman.com/brutman/netdrive/mlog"

	"github.com/gdamore/tcell/v2"
)

var timestampFormat string = "2006-01-02 15:04:05"

// Useful types

type operation_t uint8
type sessionId_t uint16
type sequence_t uint16
type macAddr_t [6]byte

func (m macAddr_t) String() string {
	return fmt.Sprintf("%02X:%02X:%02X:%02X:%02x:%02X", m[0], m[1], m[2], m[3], m[4], m[5])
}

// mlog usage
//
// Debug: Anything that might be interesting for debugging a problem.
// Info:  Events we are interest in, like people connecting, disconnecting, etc.
// Warn:  Things that should not happen, including protocol errors from clients.
//        These are generally problems outside of the server, but we want to note them.
// Error: Should not happen; read errors from files and hard errors.
//        (Truly exceptional errors caused by clients might also be in this category.)


// ServerVersion
//
// Every time the command structure changes or the behavior changes we have
// a new version.
//
// The simple thing to do is to reject connections from client that don't
// the same version number.  We are going to try to support at least two
// prior versions.
//
// Optional commands can be introduced by a new version.  Optional means that
// the command number is reserved going forward, but the server doesn't have
// to implement the command - it can return an error to the client instead.
// An example of this are the three checkpoint commands introduced in
// version 2.  A version 2 client can send them, but a server does not have to
// implement them.  (Optional commands are optional because they do not crater
// the client if they can't be used; the client continues with possibly degraded
// function, but is not broken.)
//
// Versions:
//
//  1: Connect, Disconnect, Read, Write, Write_Verify
//  2: All of version 1 plus optional Mark, Goto and List checkpoint commands.
//  3: Streaming read and write support
//
// See PKT.ASM for the more detailed changes in the command header.

// The latest version of the protocol recognized by this code.  It is recommended
// that a server recognize at least one previous version of the protocol.
const ServerVersion = 3

var versionMaxCommand = [...]operation_t{
	0,
	op_write_verify,
	op_list_checkpoints,
	op_list_checkpoints,
}

// Command data structure.  This header is always present.
// Optional bytes may follow it depending on the command.
//
// Sequence is chosen randomly by the client at startup.  Here are the rules:
//
// - The server sends back a response to the command using the same sequence number.
// - The server remembers the last sequence number seen from the client.
// - If the next command comes in and the sequence number has not moved, it is a retry.
// -   Read or write commands should be safe to re-execute on a retry.
// - If the next command comes in and the sequence number is exactly one more, the last reply was received.
// - If the next command comes in and the sequence number is more than +1, that is not possible.
// - Every time the client receives a reply from the server the sequence number advances.
//   - Except if the sequence number was wrong (not a retry or a plus 1) - then you are broken.
//
// This implies that if the server doesn't like an incoming command because
// it fails basic sanity checking, then it can do one of two things.
//
// - No respond at all.  The client will retry, never get a response, and timeout.
// - Respond with a failure code.  The client can look at the failure code and try
//   to figure out how to proceed, although our clients are generally very simple.

// op_readahead is an oddball, as it arrives mostly unsolicited and asynchronously from
// from a running command.  It's loosely tied to the last read that was sent.  I started
// a new range for it for the purpose of lumping other similar unsolicited/async messages
// together.

const (
	_ = iota
	op_connect
	op_disconnect
	op_read
	op_write
	op_write_verify
	op_mark_checkpoint
	op_goto_checkpoint
	op_list_checkpoints

	op_readahead = 128
)


// Command headers

const commandHeaderMinLen = 14
const commandHeaderV3Len = 16

// Structure fields must be exported to be manipulated by binary.Read
type command struct {
	Version   uint16
	Session   sessionId_t // Assigned by the server
	Sequence  sequence_t  // Assigned by the client; last successfully processed Seq #
	Operation operation_t
	Result    uint8
	StartSec  globals.Lba
	SecCount  uint16
	CmdExt    uint16
}

func (c command) String() string {
	return fmt.Sprintf("Ver: %d, Session: %d, Seq: %d, Cmd: %d, Res: %d, Start: %d, Count: %d",
		c.Version, c.Session, c.Sequence, c.Operation, c.Result, c.StartSec, c.SecCount)
	// return fmt.Sprintf("Ver: %02d, Session: %05d, Seq: %5d, Cmd: %2d, Res: %2d, Start: %8d, Count: %5d",
	// c.Version, c.Session, c.Sequence, c.Operation, c.Result, c.StartSec, c.SecCount)
}


// Version 1 and Version 2: 14 byte length
type command_v2 struct {
	Version   uint16
	Session   sessionId_t // Assigned by the server
	Sequence  sequence_t  // Assigned by the client; last successfully processed Seq #
	Operation operation_t
	Result    uint8
	StartSec  globals.Lba
	SecCount  uint16
}

// Version 3: 16 byte length
type command_v3 struct {
	Version   uint16
	Session   sessionId_t // Assigned by the server
	Sequence  sequence_t  // Assigned by the client; last successfully processed Seq #
	Operation operation_t
	Result    uint8
	StartSec  globals.Lba
	SecCount  uint16
	CmdExt    uint16
}


// Global file map - used to detect when a file is going to be opened multiple times

type openImagesRec_t struct {
	readonly    bool
	journalType image.JournalType
	refcount    int
}

func newOpenImageRec(oi *image.ImageFile) openImagesRec_t {
	return openImagesRec_t{
		readonly:    oi.Readonly,
		journalType: oi.JournalType,
		refcount:    1,
	}
}

var openImages map[string]openImagesRec_t

type session_t struct {
	sessionId     sessionId_t      // Session ID assigned by the server.
	macAddr       macAddr_t        // MAC addr of the client.
	netAddr       net.UDPAddr      // IP address and port of the client.
	protocol      uint16           // Client/Server protocol version
	sequence      sequence_t       // Sequence chosen by the client
	openImage     *image.ImageFile // Data structure for the open base image
	startTime     time.Time        // When did this session start?
	lastActive    time.Time        // Last active time
	blocksRead    int              // How many blocks did they read?
	blocksWritten int              // How many blocks did the write?
	retries       int              // How many retries have we seen?
	latency       time.Duration    // Detected latency
	lastCmd       command          // Last command executed for this session
	exitReason    string           // Text explaining why this exited
	clientType    uint16           // 0 is unknown, 1 is DOS
	blockSize     uint16           // Block size; not used yet
	osString      string           // Whatever they want to identify as ...
	checkpoints   []string         // A cached list of checkpoints, if applicable
}

// updateSequence
//
// Only increment our sequence number if the client has incremented theirs.
// That indicates that there was a successful round-trip on the last command.
//
// If the client has not incremented the sequence number then one of two
// things has happened:
//
// 1. It's a retry of the last commmand, and the packet will look the same.
//    (We don't save the payload, so there can be a difference there.)
//
// 2. They got a timeout on the last command and so they never incremented
//    their sequence number.  And then they decided to do something different
//    so now you have a reused sequence number with a different command
//    and payload.
//
// In the first situation we want to know if it's a retry of the original
// command because we don't want to re-execute the command.  Things like
// Mark Checkpoint and Goto Checkpoint probably work if they are reexecuted,
// but will have side effects like having a duplicate checkpoint marker.
//
// In the second situation we need to execute the new command.  We may have
// executed the original command, but it's too late to undo it.  (The client
// needs to be aware that just because they got a timeout doesn't mean that
// the operation didn't happen; they just didn't get a positive response.
//
// So the return values here are:
//
// * Is this really a retry of a previously executed command with the same
//   sequence number? (True or False)
// * Is this just an error because the sequence number is totally wrong.

func (s *session_t) updateSequence(cmd command) (bool, error) {

	switch {

	case cmd.Sequence == s.sequence:

		// Same sequence number; is this a retry of the last command or something different?

		if s.lastCmd == cmd {
			s.retries++
			return true, nil // It's a legit retry.
		} else {
			return false, nil // They gave up and are trying something different.
		}

	case cmd.Sequence == s.sequence+1:
		s.sequence++
		return false, nil

	default:
		mlog.Error("Serve:updateSequence: Bad sequence number, addr: %v, ours: %v, theirs: %v, cmd: %v\n",
			s.netAddr.AddrPort(), s.sequence, cmd.Sequence, cmd)
		return false, errors.New("Bad sequence")
	}

}

func (s session_t) String() string {
	var mode string
	if s.openImage.Readonly == false {
		mode = "(RW)"
	} else {
		mode = "(RO)"
	}
	return fmt.Sprintf("%5d, %v, %s:%d, %s %s, Started: %s, Last seen: %s, Blocks Read: %d, Blocks Written: %d, Retries: %d, Latency: %d, OS Ver: %v",
		s.sessionId, s.macAddr, s.netAddr.IP.To4(), s.netAddr.Port, s.openImage.Filename, mode,
		s.startTime.Format(timestampFormat), s.lastActive.Format(timestampFormat),
		s.blocksRead, s.blocksWritten, s.retries, s.latency.Milliseconds(),
		s.osString)
}

// Session tracking.
//
// Commands for a session should always be processed serially.  With UDP it
// is possible to get a duplicate packet, so ensure things are serial by
// routing all commands for a session to one goroutine dedicated for that
// session.

type sessionRec_t struct {
	session *session_t  // Pointer to a Session
	cmdChan chan udpCmd // Channel for sending commands to the session
}

// Global map for looking up sessions
var sessions map[sessionId_t]sessionRec_t

var mapUpdateLock = sync.RWMutex{}

type udpCmd struct {
	conn    *net.UDPConn
	addr    *net.UDPAddr
	cmd     command
	optData []byte
}

// Data structure for sending UDP packets to the UDP processor on a channel.
//
// You can't select on both a socket and a channel in Go, so convert the
// results of a UDP packet read into something that can be sent on a channel.
// That way the real UDP processing routine an select on both the UDP
// channel and a signal channel so that it can know when to shut down.

type udpPkt struct {
	conn *net.UDPConn
	addr *net.UDPAddr
	buf  []byte
}

var sessionHandlerWg sync.WaitGroup

// Life of a session
//
// All UDP packets are read by one goroutine.  That goroutine sends the UDP command
// to another goroutine on a channel.
//
// The second goroutine is the UDP processor.  It looks up the session id:
//
// * If this is a new session, doConnect is called.  If that is successful
//   a new goroutine is created just to handle that session.
// * If this is an existing session we find the channel for it in the Sessions map
//   and send it the UDP packet for processing.
//
// So there is a goroutine to read the wire, a goroutine to route the UDP packets,
// and a goroutine for each active session.
//
// To end a session call retireSession.  Retire session removes the session
// from the session map and closes the channel, telling the session goroutine to exit.

func retireSession(targetSessionId sessionId_t, reason string) error {

	mapUpdateLock.Lock()

	targetSession, exists := sessions[targetSessionId]

	if exists == false {
		mapUpdateLock.Unlock()
		mlog.Warn("Serve:retireSession: invalid session number: %v\n", targetSessionId)
		return fmt.Errorf("retireSession: invalid session number %v", targetSessionId)
	}

	// Remove the session from the global map to hide it from future work.
	delete(sessions, targetSessionId)

	// Decrement the reference count in the open files map
	of := openImages[targetSession.session.openImage.Filename]
	of.refcount--
	if of.refcount == 0 {
		delete(openImages, targetSession.session.openImage.Filename)
	} else {
		openImages[targetSession.session.openImage.Filename] = of
	}

	mapUpdateLock.Unlock()

	// At this point processUdp will never see this session again so it can't
	// send new commands to it.  (When it does a session lookup it also uses
	// the mapUpdateLock to avoid a race condition with this code.)
	//
	// The channel is buffered so any last commands sent to the channel will
	// still be in it.  We close the channel here as a signal to end that
	// processing loop.

	targetSession.session.exitReason = reason

	close(targetSession.cmdChan)

	mlog.Info("Serve:retireSession: Session %v is ending: %v\n", targetSessionId, reason)
	return nil

}

// Stats

type stats_t struct {
	serverStarted         time.Time
	connectsGood          int
	connectsFailed        int
	disconnects           int
	blocksRead            int
	blocksWritten         int
	retriesRead           int
	retriesWrite          int
	sessionTimeouts       int
	maxConcurrentSessions int
}

func (s stats_t) String() string {
	return fmt.Sprintf(
		`
Server stats:
  Current time: %v, Started: %v, Uptime: %v
  Connects: Good: %d, Failed: %d, Disconnects: %d
  Blocks: Read: %d, Written: %d
  Retries: Reads: %d, Writes: %d
  Session timeouts: %d, Most concurrent sessions: %d
`,
		time.Now().Format(timestampFormat),
		s.serverStarted.Format(timestampFormat), time.Since(s.serverStarted).Round(time.Second),
		s.connectsGood, s.connectsFailed, s.disconnects,
		s.blocksRead, s.blocksWritten,
		s.retriesRead, s.retriesWrite, s.sessionTimeouts, s.maxConcurrentSessions)
}

var stats stats_t

func serveUsage() {
	fmt.Println(
		`
Format for the serve command is:

  netdrive [common flags] serve [serve flags]


Common flags:

  -log_file  <filename>            Set a filename to log to, default is no logging
  -log_level <level>               Log level (debug|info|warn|error), default is info

Optional serve flags:

  -headless                        Run in headless (non-interactive) mode
  -image_dir <i>                   Directory for images, default is the current dir
  -port <n>                        Serve using UDP port n (default is port 2002)
  -bind_addr <ip_addr>             Listen only on the ip_addr network interface (default is all)
  -timeout <n>                     Set the session timeout in minutes (default is unlimted)
  -max_time <n>                    Set session max time in minutes (default is unlimited)
  -max_active_sessions <n>         Max active sessions (default is 20)
  -session_scoped_writes_dir <d>   Dir to use for session_scoped writes log files
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

// Command line flags

var f_ImageDir string
var f_BindAddr string
var f_ServerPort int
var f_SessionTimeout int
var f_SessionMaxTime int
var f_MaxActiveSessions int

var f_SessionScopedWritesDir string
var f_SessionScopedWritesLBALimit int

var f_TortureTest bool

var f_Headless bool

// Serve is the entry point to the image server.  It opens a listening UDP port,
// sets up the text user interface (if not running headless), and serves requests
// from clients.
func Serve(args []string) (rc error) {

	defer func() {
		if rc != nil {
			fmt.Println("Error:", rc)
		}
		if _, ok := rc.(*usageError); ok == true {
			serveUsage()
		}
	}()

	serveFlags := flag.NewFlagSet("serve flags", flag.ExitOnError)

	serveFlags.StringVar(&f_ImageDir, "image_dir", "", "Directory for disk images")
	serveFlags.IntVar(&f_ServerPort, "port", 2002, "Server UDP port")
	serveFlags.StringVar(&f_BindAddr, "bind_addr", "0.0.0.0", "IP address to bind to")
	serveFlags.IntVar(&f_SessionTimeout, "timeout", 0, "Session timeout in minutes")
	serveFlags.IntVar(&f_SessionMaxTime, "max_time", 0, "Session maximum time.")
	serveFlags.IntVar(&f_MaxActiveSessions, "max_active_sessions", 20, "Maximum number of active sessions")

	serveFlags.StringVar(&f_SessionScopedWritesDir, "session_scoped_writes_dir", "", "Directory for session scoped writes files")
	serveFlags.IntVar(&f_SessionScopedWritesLBALimit, "ssw_unique_lbas_limit", 0, "Maximum number of unique LBAs for a session")

	serveFlags.BoolVar(&f_TortureTest, "inject_errors", false, "Trust, everything is perfectly safe.")

	serveFlags.BoolVar(&f_Headless, "headless", false, "Run in headless mode.")

	serveFlags.Usage = serveUsage

	err := serveFlags.Parse(args)
	if err != nil {
		return &usageError{"Error parsing flags for serve"}
	}

	if len(serveFlags.Args()) > 0 {
		switch strings.ToLower(args[0]) {
		case "help":
			serveUsage()
			return nil
		default:
			return &usageError{fmt.Sprintf("Unknown command: %s", args[0])}
		}
	}

	// Ensure we can touch directories if they were specified

	workingDir, err := os.Getwd()
	if err != nil {
		return errors.New("Unable to determine the current working directory.")
	}

	if f_ImageDir != "" {
		fileInfo, err := os.Stat(f_ImageDir)
		if err != nil {
			return fmt.Errorf("Unable to stat f_ImageDir: %s", f_ImageDir)
		}
		if !fileInfo.Mode().IsDir() {
			return fmt.Errorf("Specified f_ImageDir is not a directory: %s", f_ImageDir)
		}
	}

	if f_SessionScopedWritesDir != "" {
		fileInfo, err := os.Stat(f_SessionScopedWritesDir)
		if err != nil {
			return fmt.Errorf("Unable to stat f_SessionScopedWritesDir: %s", f_SessionScopedWritesDir)
		}
		if !fileInfo.Mode().IsDir() {
			return fmt.Errorf("Specified f_SessionScopedWritesDir is not a directory: %s", f_SessionScopedWritesDir)
		}
	}

	CrcInit( )

	sessions = make(map[sessionId_t]sessionRec_t, 10)
	openImages = make(map[string]openImagesRec_t, 10)

	bindAddr := net.ParseIP(f_BindAddr)
	if bindAddr == nil {
		return fmt.Errorf("Error parsing bind IP address: address: %v\n", f_BindAddr)
	}

	// Ensure we can listen on the selected UDP port.
	addr := net.UDPAddr{
		IP:   bindAddr,
		Port: f_ServerPort,
	}
	conn, err := net.ListenUDP("udp4", &addr)

	if err != nil {
		return fmt.Errorf("Error listening on UDP port %v: %s\n", addr, err)
	}
	defer conn.Close()

	// Signal handling channel; get notified about the basic signals we need to shut down cleanly.
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, os.Kill, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGINT)

	// Screen event handling channel; tcell events will be sent on this channel.
	// Use this instead of tcell.PollEvent as we want to listen for signals too
	// so we need to use select on two different channels.
	//
	// If we are running headless that is fine, as this channel just will not
	// receive any events.

	var screenEventCh chan tcell.Event

	if f_Headless == false {

		screenEventCh, err = initConsole()
		if err != nil {
			return err
		}

		// If we have no console then there is no point in having an info message buffer.
		// We'll still use the channels for info message processing but they will be
		// effectively no-ops, which is better than having if-else statements all over.
		infoMsgs = make([]string, 0, 10000)

	} else {
		fmt.Println("Headless mode specified - use Ctrl-C to exit.")
	}

	// Launch the info message handling thread.  Be sure to wait for it before exiting.

	infoMsgChannel = make(chan string, 100)
	infoMsgChannelExit = make(chan struct{})
	infoMsgChannelDone = make(chan struct{})
	go addMsg()

	// Make a special version of infoMsgChannel that prepends a timestamp on messages.
	infoMsgChannelTs = make(chan string, 20)

	go func() {
		for str := range infoMsgChannelTs {
			str = time.Now().Format(timestampFormat) + " " + str
			infoMsgChannel <- str
		}
	}()

	stats.serverStarted = time.Now()

	infoMsgChannel <- "Server settings:"

	if f_ImageDir == "" {
		infoMsgChannel <- "  Image directory: " + workingDir
	} else {
		infoMsgChannel <- "  Image directory: " + f_ImageDir
	}

	infoMsgChannel <- fmt.Sprintf("  Max session length: %d minutes, Session timeout: %d minutes, Max active sessions: %d\n",
		f_SessionMaxTime, f_SessionTimeout, f_MaxActiveSessions)

	if f_SessionScopedWritesDir == "" {
		infoMsgChannel <- "  Session scoped writes directory: " + workingDir
	} else {
		infoMsgChannel <- "  Session scoped writes directory: " + f_SessionScopedWritesDir
	}

	infoMsgChannel <- fmt.Sprintf("  Listening on %v:%d", bindAddr, f_ServerPort)
	mlog.Info("Serve: Listening on %v:%d\n", bindAddr, f_ServerPort)

	infoMsgChannel <- "\nUse the \"help\" command to get help"

	// Udp packet reading.  This runs in the background and takes incoming
	// UDP messages and converts them to structs than can be passed on a
	// channel.  A second go-routine can then select on that channel or a
	// control channel that tells it when to shut down.

	processUdpInputChan := make(chan udpPkt, 20)
	processUdpExitChan := make(chan struct{})
	processUdpEnded := make(chan struct{})

	// Read incoming UDP packets and send them on a channel.  This will run for the
	// lifetime of the program so we don't bother with the logic to shut it down cleanly.
	go func() {
		for {
			buf := make([]byte, 40000)
			n, addr, err := conn.ReadFromUDP(buf)
			if err != nil {
				continue
			}
			u := udpPkt{conn, addr, buf[:n]}
			processUdpInputChan <- u
		}
	}()

	// Read either the UDP packet channel or the control channel.  If we
	// get something on the control channel it's time to end.
	go func() {
		done := false
		for done == false {
			select {
			case v := <-processUdpInputChan:
				processUdp(v.conn, v.addr, v.buf)
			case <-processUdpExitChan:
				// This happens whether we send something or we close the channel.
				done = true
			}
		}
		// We are ending.  Close the UDP connection and indicate that this exited.
		infoMsgChannelTs <- "UDP processing shutting down."
		conn.Close()
		close(processUdpEnded)
	}()

	// Another background thread to harvest sessions that have timed out or exceeded
	// their maximum time.  This also runs for the life of the server.
	go func() {
		for {
			time.Sleep(30 * time.Second)

			deadPool := make([]sessionId_t, 0, 20)
			mapUpdateLock.RLock()
			for _, v := range sessions {
				if f_SessionTimeout > 0 {
					if time.Since(v.session.lastActive) > time.Duration(f_SessionTimeout)*time.Minute {
						deadPool = append(deadPool, v.session.sessionId)
					}
				}
				if f_SessionMaxTime > 0 {
					if time.Since(v.session.startTime) > time.Duration(f_SessionMaxTime)*time.Minute {
						deadPool = append(deadPool, v.session.sessionId)
					}
				}

			}
			mapUpdateLock.RUnlock()

			for _, v := range deadPool {
				infoMsgChannelTs <- fmt.Sprintf("Timeout or time limit for session: %v\n", v)
				mlog.Info(fmt.Sprintf("Serve: Timeout or time limit for session: %v\n", v))
				stats.sessionTimeouts++
				if err := retireSession(v, "timeout or session time limit"); err != nil {
					mlog.Warn(fmt.Sprintf("Serve: Tried to retire an invalid session during timeout processing: %v\n", v))
				}
			}
		}
	}()

	evLoopDone = false
	quitCount = 0

	cmdInput = make([]byte, 0, 80)
	cmdInputLen = 0

	if f_Headless == true {

		// Just block and wait for a signal to tell us when to end.
		<-sigChan
		fmt.Println("Signal received, shutting down:")

	} else {

		for evLoopDone == false {

			screen.ShowCursor(cmdInputLen, max_y-1)
			screen.Show()

			select {
			case <-sigChan:
				infoMsgChannelTs <- "Signal received, shutting down:"
				evLoopDone = true
			case ev := <-screenEventCh:
				handleTcellEvent(ev)
			}

		} // end evloop

	}

	// The program is ending.  Time to clean-up.

	// Stop UDP packet processing and wait for the packet processor to exit.
	// That way we know the session handlers are not getting new work.  (No
	// values are sent on the channels; we're just using them closing to
	// synchronize.)
	close(processUdpExitChan)
	<-processUdpEnded

	// Clean out active sessions.  (No new ones can be created, so getting all
	// of the session IDs and retiring them will not miss anything.)
	infoMsgChannelTs <- "Ending active sessions."

	deadPool := make([]sessionId_t, 0, 20)
	mapUpdateLock.RLock()
	for _, v := range sessions {
		deadPool = append(deadPool, v.session.sessionId)
	}
	mapUpdateLock.RUnlock()

	for _, v := range deadPool {
		retireSession(v, "server is ending")
	}

	// Wait for the session handler threads to be done.
	sessionHandlerWg.Wait()

	// Tell the info message handler to stop processing new info messages and
	// wait for it to tell us that it has exited.
	close(infoMsgChannelExit)
	<-infoMsgChannelDone

	if f_Headless == false {
		screen.Sync()
		screen.Fini()
	}

	mlog.Info(stats.String())
	mlog.Info("Server shutdown.\n")
	fmt.Println("The mTCP NetDrive Server has shut down.")

	return nil
}

// processUdp handles and dispatches all incoming UDP packets.
// This runs in a single goroutine.
func processUdp(conn *net.UDPConn, addr *net.UDPAddr, buf []byte) {

	if len(buf) < commandHeaderMinLen {
		mlog.Debug("Serve:processUdp: Received something too short to be a client command, addr: %v, bytes: %v\n", addr.AddrPort(), buf)
		return
	}


	// Figure out the version we have.  The only difference at the moment is
	// that v3 is two bytes longer because of two padding bytes.  The first
	// 14 bytes are common.

	vers := uint16(buf[0]) + uint16(buf[1])*256

	// Use a reader to deserialize the header data.  Use the v2 command header
	// which is the same as the v3 command header, except for an extra word.
	r := bytes.NewReader(buf)

	var c_tmp command_v2
	err := binary.Read(r, binary.LittleEndian, &c_tmp)
	if err != nil {
		mlog.Error("Serve:processUdp: Error reading fixed command portion from v1 or v2 UDP packet, addr: %v, error: %v\n", addr.AddrPort(), err)
		return
	}


	var c command
	c.Version = c_tmp.Version
	c.Session = c_tmp.Session
	c.Sequence = c_tmp.Sequence
	c.Operation = c_tmp.Operation
	c.StartSec = c_tmp.StartSec
	c.SecCount = c_tmp.SecCount

	// If we have a v3 command header read the extra field.
	if vers == 3 {
		if len(buf) < commandHeaderV3Len {
			mlog.Error("Serve:processUdp: Error reading fixed command portion from v3 UDP packet, addr: %v, error: %v\n", addr.AddrPort(), err)
			return
		} else {
	    var CmdExt uint16
			err := binary.Read(r, binary.LittleEndian, &CmdExt)
			if err != nil {
				// This should never happen, as we already checked the length.
				mlog.Error("Serve:processUdp: Error reading CmdExt from v3 command header. %v\n", err)
			}
			c.CmdExt= CmdExt
		}
	}

	// Room for a 32KB write with some extra padding
	optData := make([]byte, 32*1024 + 1024)
	optDataLen, err := r.Read(optData)
	if err != nil && err != io.EOF {
		mlog.Error("Serve:processUdp: Error reading payload from UDP packet, addr: %v, error: %v\n", addr.AddrPort(), err)
		return
	}

	// Truncate optData it to what we actually received.
	optData = optData[:optDataLen]

	mlog.Debug("Serve:processUdp: Incoming command, addr: %v, cmd: %v, optDataLen: %v\n", addr.AddrPort(), c, optDataLen)


	// Quick safety checks

	// If the server gets a version number it does not handle it should
	// respond with its version number.  The client will understand that
	// to be a version mismatch.  Reading the error message is not needed
	// when that happens.
	//
	// This is mostly like to happen on a new connection.
	if (c.Version < 1) || (c.Version > ServerVersion) {
		mlog.Debug("Serve:processUdp: Bad protocol version, addr: %v, cmd: %v\n", addr.AddrPort(), c)
		resp := command{Version: ServerVersion, Session: c.Session, Sequence: c.Sequence, Operation: op_connect, CmdExt: c.CmdExt, Result: 1}
		sendResp(conn, addr, resp, []byte("Bad protocol version"))
		return
	}

	u := udpCmd{conn, addr, c, optData}

	if c.Operation == op_connect {

		if c.Session != 0 {
			mlog.Debug("Serve:processUdp: Protocol error, connect with non-zero session: addr: %v\n", addr.AddrPort())
			resp := c
			resp.Result = 1
			sendResp(conn, addr, resp, []byte("Protocol error: Connect with non-zero session"))
			return
		}

		// Fixme: We're not going to handle a retry here well and that should be fixed.
		//
		// * If it's a duplicate UDP packet and the client already connected,
		//   this duplicate will clear out the old connection.  But the client will get
		//   confused by the new response with a different session number, so it will
		//   ignore it.  Meanwhile, the old session will be dead.
		//
		// * If it's a duplicate packet and the client did not see a response then we
		//   get lucky, and this packet gets processed and this becomes the session.
		//
		// There is not a great solution and it's rare.

		newSession, err := doConnect(u)
		if err == nil {

			// Create a session handler function just for this session.
			// From this point forward, when we see this session ID we want to call this specific function.

			// The channel needs to be buffered to avoid stalling this thread.
			ch := make(chan udpCmd, 5)
			newHandler := newSessionHandler(&newSession, ch)

			sessionHandlerWg.Add(1)
			go newHandler()

			// New handler is running; make it visible.
			mapUpdateLock.Lock()
			sessions[newSession.sessionId] = sessionRec_t{&newSession, ch}
			if len(sessions) > stats.maxConcurrentSessions {
				stats.maxConcurrentSessions = len(sessions)
			}

			of, exists := openImages[newSession.openImage.Filename]
			if exists == true {
				// Must have been read-only if we allowed this.
				of.refcount++
			} else {
				// First time for either read-only or read-write files.
				of = newOpenImageRec(newSession.openImage)
			}
			openImages[newSession.openImage.Filename] = of

			mapUpdateLock.Unlock()

			stats.connectsGood++

		} else {
			mlog.Info("Serve:processUdp: Failed connect, addr: %v, error: %v\n", addr.AddrPort(), err)
			infoMsgChannelTs <- fmt.Sprintf("Failed connect: %v: %s\n", addr.IP.To4(), err)
			resp := command{Version: c.Version, Session: c.Session, Sequence: c.Sequence, Operation: op_connect, CmdExt: c.CmdExt, Result: 1}
			sendResp(conn, addr, resp, []byte(err.Error()))
			stats.connectsFailed++
		}

	} else {

		// Grab the mapUldateLock to ensure that we don't try to close the
		// channel in another thread while we are trying to send a value to it.
		// That will cause a panic if it happens.
		//
		// The channel is buffered so under reasonable usage that will not block
		// this thread.  There is a danger that the session processing thread
		// gets hung up and blocks this thread too, but that would be
		// extraordinary.  (If this was a highly reliable web service spanning
		// machines using RPCs we'd have to implement RPC deadlines.  But it's
		// not.  However the filesystem being slow is a potential weakness.)

		mapUpdateLock.RLock()

		s, exists := sessions[c.Session]
		if exists == false {
			r := command{Version: c.Version, Session: c.Session, Sequence: c.Sequence, Operation: c.Operation, CmdExt: c.CmdExt, Result: 1}
			mlog.Debug("Serve:processUdp: Bad session, addr: %v, session: %v\n", addr.AddrPort(), c.Session)
			sendResp(conn, addr, r, []byte("Bad session"))
		} else {
			s.cmdChan <- u
		}

		mapUpdateLock.RUnlock()
	}

}

// Create a session handler.  Each session handler handles all of the
// commands for one session, ensuring that they are serialized and do
// not try to run in parallel.  Processing ends when the channel is
// closed.

func newSessionHandler(s *session_t, inputChannel <-chan udpCmd) func() {

	// Return a function that listens on a channel and processes one
	// command at a time until the channel closes.
	return func() {

		defer sessionHandlerWg.Done()

		processCommands := true

		for u := range inputChannel {

			if processCommands == false {
				// Used when we are draining the channel and don't want to process commands.
				continue
			}

			// Basic check to ensure the client didn't do something weird, like
			// change the protocol version.  Just ignore it outright.  Trying
			// to send an error response means we tolerated it and we should not.
			//
			// Argh ..  this seemed like a good idea at the time but I have a bug
			// in an older device driver where version 2 clients send commands
			// as if they were version 1.  So don't enforce this yet, or only selectively
			// enforce it on version 3 and up.

			/*
			if u.cmd.Version != s.protocol {
				mlog.Debug("Serve:sessionHandler: Protocol error, new command version doesn't match session version: addr: %v\n", u.addr.AddrPort())
				continue
			}
			*/


			// If the time since the last command is shorter than the previous
			// record, update our latency.  Eventually we'll get two back-to-back
			// commands and that will approximate the actual latency.
			if time.Since(s.lastActive) < s.latency {
				s.latency = time.Since(s.lastActive)
			}

			s.lastActive = time.Now()

			// If the sequence number matches the sequence number of the last command
			// then this is a retry and the command should look exactly the same.
			//
			// A client might try to be clever, think a command timed-out and send
			// a new command without bumping the sequence number.  That is not
			// allowed.  They need to wait for the command to complete, which means
			// getting an answer from the server (either good or bad) and then
			// advancing the sequence number before issuing another command.

			if (s.sequence == u.cmd.Sequence) && (s.lastCmd != u.cmd) {
				mlog.Debug("Serve:sessionHandler: Session %v reused a sequence number but was not a retry, addr: %v old cmd: %v retry cmd: %v\n",
										s.sessionId, u.addr.AddrPort(), s.lastCmd, u.cmd)
				resp := u.cmd
				resp.Result = 1
				sendResp(u.conn, u.addr, resp, []byte("Protocol error: Old sequence number reused on a different command"))
				continue
			}

			fatal := false

			// Check for optional commands this version of the protocol doesn't understand.
			// If we do find one of these, treat it like a normal command that we run, but
			// it just returns an error.
			if u.cmd.Operation == 0 || u.cmd.Operation > versionMaxCommand[u.cmd.Version] {
				doBadCommand(u, s)
			} else {

				switch u.cmd.Operation {
				case op_disconnect:
					doDisconnect(u, s)
				case op_read:
					doRead(u, s)
				case op_write, op_write_verify:
					doWrite(u, s)
				case op_mark_checkpoint:
					doMarkCheckpoint(u, s)
				case op_goto_checkpoint:
					err := doGotoCheckpoint(u, s)
					if e, ok := err.(*globals.FatalError); ok {
						mlog.Error("Serve:sessionHandler: GotoCheckpoint failed, ending the session, session: %v, error: %v\n", s.sessionId, e)
						fatal = true
					}
				case op_list_checkpoints:
					doListCheckpoints(u, s)
				}

			}

			s.lastCmd = u.cmd

			// retireSession is going to remove the session from the session map, ensuring
			// that no more commands can be sent on the channel.  Fatal means do an immediate
			// stop, so we don't want to process any further commands.  But we still want to
			// keep processing the inputChannel until it is drained.
			if fatal == true {
				processCommands = false
				retireSession(s.sessionId, "fatal error")
			}

		} // end for - the inputChannel is drained.

		// Close the file and and its journal (if applicable) after we know that
		// no further work can be processed by this goroutine.
		if err := s.openImage.Close(); err != nil {
			infoMsgChannelTs <- fmt.Sprintf("Error closing image or journal file %s for session %d\n", s.openImage.Filename, s.sessionId)
		}

		mlog.Info("Serve:sessionHandler: Handler for session %v has exited, session: %v.\n", s.sessionId, s)
		infoMsgChannelTs <- fmt.Sprintf("Session handler for %d exited: %v\n", s.sessionId, s.exitReason)
	}

}

func sendResp(conn *net.UDPConn, addr *net.UDPAddr, r command, p []byte) {

	// Create a new bytes.Buffer which implements a Writer.  Then you can use
	// binary.Write to write the data structure and take care of the encoding.
	// Finally, the bytes.Buffer is still yours to add to before sending it oout.

	// mlog.Debug("Serve:sendResp: Addr: %v, resp: %v, optDataLen: %d, optData: %v\n", addr.AddrPort(), r, len(p), p)

	outbuf := new(bytes.Buffer)

	if (r.Version < 3) {
		var c command_v2
		c.Version = r.Version
		c.Session = r.Session
		c.Sequence = r.Sequence
		c.Operation = r.Operation
		c.Result = r.Result
		c.StartSec = r.StartSec
		c.SecCount = r.SecCount
		binary.Write(outbuf, binary.LittleEndian, c)
	} else {
		binary.Write(outbuf, binary.LittleEndian, r)
	}

	outbuf.Write(p)

	conn.WriteToUDP(outbuf.Bytes(), addr)
}

// doBadCommand is used when the client sends us a command that we don't know.
// Instead of just ignoring the command or killing the session we treat these
// as optional, unsupported commands for this version of the protocol.
// If we don't do something like this we'll be constantly bumping the protocol
// version number, making n-1 support difficult.
//
// When this happens treat it like a supported command, but return that an
// error happened.  Advance the sequence number just like on a normal command.
// That way the client can test to see if an optional command works and continue
// without having to play games with the sequence number.

func doBadCommand(u udpCmd, s *session_t) {

	mlog.Warn("Serve:doBadCommand: Unknown or optional operation for protocol version, addr: %v, cmd: %v\n", u.addr.AddrPort(), u.cmd)

	// Basically echo everything back to them but with an advanced sequence
	// number.

	resp := command{
		Version:   s.protocol,
		Session:   s.sessionId,
		Sequence:  u.cmd.Sequence,
		StartSec:  u.cmd.StartSec,
		SecCount:  u.cmd.SecCount,
		Operation: u.cmd.Operation,
		CmdExt:    u.cmd.CmdExt,
		Result:    1,
	}

	// We don't care if this is a retry; we don't recognize it.
	if _, err := s.updateSequence(u.cmd); err != nil {
		sendResp(u.conn, u.addr, resp, []byte("Bad sequence"))
		return
	}

	sendResp(u.conn, u.addr, resp, []byte("Unknown command"))
}

func isAlphaNumeric(c rune) bool {
	return ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || ('0' <= c && c <= '9')
}

func isSafeFilename(fn string) bool {

	// Basic validation on the requested image name:
	//
	// Ideally the requested image is a logical name that we map to an image on
	// the server.  But for now it's just a raw filename from DOS.  Limit the
	// raw filename to reasonable characters.
	//
	// For security purposes do not allow:
	//
	//   A leading '.'
	//   A leading os.PathSeparator
	//   A leading char that might cause trouble ('_')
	//   Two '.'s in a row anywhere.
	//   Two os.PathSeparators in a row anywhere.

	invalidCharFound := false

	// fn is a string now so we should use runes ...
	for i, c := range fn {
		if i == 0 {
			if (isAlphaNumeric(c) || (c == '_')) == false {
				invalidCharFound = true
				break
			}
		} else {
			if (isAlphaNumeric(c) || (c == '.') || (c == '_') || (c == '-') || (c == os.PathSeparator)) == false {
				invalidCharFound = true
				break
			}
		}
	}

	// Scan for two '.'  or path separators in a row
	lastChar := ' '
	for _, c := range fn {
		if (c == os.PathSeparator || c == '.') && (lastChar == c) {
			invalidCharFound = true
			break
		}
		lastChar = c
	}

	return invalidCharFound == false
}

// Given a request for a new connection check the file and create a session.
//
// Only the UDP processing thread calls doConnect so we don't have to worry
// about concurrency issues.
//
// Return value: a session record and an error.  The session record is only
// valid if the error is nil.
//
// On an error the error response will use the optional payload to give a
// human readable error message to the client.

func doConnect(u udpCmd) (session_t, error) {

	var sr session_t

	mlog.Debug("Serve:doConnect: Cmd and payload, addr: %v, cmd: %v, payload: %v\n", u.addr.AddrPort(), u.cmd, u.optData)

	// Session must be zero on a connect message.
	if u.cmd.Session != 0 {
		mlog.Debug("Serve:doConnect: Protocol error: New sessions should start with session set to 0.\n")
		return sr, errors.New("protocol error")
	}

	if len(sessions) >= f_MaxActiveSessions {
		mlog.Debug("Serve:doConnect: Too many active connections; newest connection refused.\n")
		return sr, errors.New("too many connections")
	}

	if ((u.cmd.Version == 1) && (len(u.optData) < 8)) || ((u.cmd.Version >= 2) && (len(u.optData) < 52)) {
		mlog.Debug("Serve:doConnect: Payload for doConnect too short, addr: %v, payload len: %v\n", u.addr.AddrPort(), len(u.optData))
		return sr, errors.New("protocol error")
	}

	// MAC address is always first
	mac := macAddr_t(u.optData[0:6])

	fn := ""
	fnFieldStart := 6

	var clientType uint16
	var blockSize uint16
	var osString string

	if u.cmd.Version >= 2 {

		clientType = uint16(u.optData[6]) + uint16(u.optData[7])*256
		blockSize = uint16(u.optData[8]) + uint16(u.optData[9])*256

		osField := u.optData[10:50]
		nulCharIndex := bytes.IndexByte(osField[0:], 0)
		if nulCharIndex == -1 {
			mlog.Warn("Serve:doConnect: No null char found terminating the OS string, addr: %v, payload: %v\n", u.addr.AddrPort(), u.optData)
			return sr, errors.New("protocol error")
		}
		osString = string(osField[0:nulCharIndex])

		fnFieldStart = 50

	}

	// Nastyiness to convert the fixed length byte array to a string.
	fnField := u.optData[fnFieldStart:]
	nulCharIndex := bytes.IndexByte(fnField[0:], 0)
	if nulCharIndex == -1 {
		mlog.Warn("Serve:doConnect: No null char found terminating the filename, addr: %v, payload: %v\n", u.addr.AddrPort(), u.optData)
		return sr, errors.New("protocol error")
	}

	fn = string(fnField[0:nulCharIndex])

	mlog.Debug("Serve:doConnect: Connection request, addr: %v, client MAC: %v, image: %v\n", u.addr.AddrPort(), mac, fn)

	if isSafeFilename(fn) == false {
		mlog.Warn("Serve:doConnect: Invalid filename requested, addr: %v, payload: %v\n", u.addr.AddrPort(), u.optData)
		return sr, errors.New("bad characters in image name")
	}

	// Add the leading directory if it was specfied.
	if f_ImageDir != "" {
		fn = f_ImageDir + string(os.PathSeparator) + fn
	}

	// Check to see if there is an existing session with this MAC address
	// and virtual hard drive name.  If there is the client probably crashed
	// or rebooted without having a chance to disconnect.  If that happens,
	// clear the old session so they can re-attach more easily.
	//
	// On second thought, make the user explicitly reconnect.  This was a
	// nice feature during development but if the user tries to reconnect
	// with a new drive letter the old one lingers, which is confusing.

	/*
		oldSessionFound := false
		var oldSessionId sessionId_t

		mapUpdateLock.RLock()
		for i, v := range sessions {
			if v.session.macAddr == mac && v.session.openImage.Filename == fn {
				oldSessionFound = true
				oldSessionId = i
				mlog.Info(fmt.Sprintf("Serve:doConnect: Old session %v (%v) connected to %s found, clearing it out\n", oldSessionId, mac, fn))
				infoMsgChannelTs <- fmt.Sprintf("Old session %v (%v) connected to %s found, clearing it out\n", oldSessionId, mac, fn)
				break
			}
		}
		mapUpdateLock.RUnlock()

		if oldSessionFound {
			retireSession(oldSessionId, "stale session found, reconnecting")
		}
	*/

	// Is this file already open?
	//
	// Journaled files: bad because the journal can't be shared.
	// Read-write without session scoped writes: bad because the file can't be shared.
	// Session scoped writes: Good assuming above conditions are met.

	mapUpdateLock.RLock()
	of, exists := openImages[fn]
	mapUpdateLock.RUnlock()

	if exists == true {
		if of.journalType == image.GoBack {
			mlog.Debug("Serve:doConnect: Attempted to open a journaled file a second time, addr: %v, filename: %s\n", u.addr.AddrPort(), fn)
			return sr, fmt.Errorf("can not open journaled file %s again", fn)
		}
		if (of.readonly == false) && (of.journalType == image.NoJournal) {
			mlog.Debug("Serve:doConnect: Attempted to open a read-write file a second time, addr: %v, filename: %s\n", u.addr.AddrPort(), fn)
			return sr, fmt.Errorf("can not open read-write file %s again", fn)
		}
	}

	// Attempt to open the image file and the journal if one exists.
	openStart := time.Now()
	openImage, err := image.OpenImage(fn)
	if err != nil {
		mlog.Debug("Serve:doConnect: Error opening image, addr: %v, image: %v, error: %v\n", u.addr.AddrPort(), fn, err)
		return sr, err
	}
	elapsed := time.Since(openStart).Round(time.Millisecond)

	if elapsed.Milliseconds() > 1000 {
		mlog.Warn("Serve:doConnect: Image open time was long, addr: %v, name: %v, elapsed: %v\n", u.addr.AddrPort(), fn, elapsed)
		infoMsgChannelTs <- fmt.Sprintf("Warning: Image %s took %v to open; a client might time out.  If it is journaled you should do file maintenance.\n", fn, elapsed)
	}

	if openImage.JournalType == image.GoBack {
		if openImage.Journal.JournalRecords() > 65536 {
			infoMsgChannelTs <- fmt.Sprintf("** WARNING **: Journal for image %s is getting large; consider doing file maintenance to shrink it.\n", fn)
		}
	}

	// If we got here then the file is open.  Don't add it to the openImages map
	// until we know the session is going to be fully created.
	//
	// From this point forward, if anything goes wrong close the file.

	// Read the first sector to get the BIOS Parameter Block (BPB).  Everything
	// except DOS 1.x diskettes will have a valid BPB here.

	firstSector, err := openImage.ReadLbas(0, 1, 0)
	if err != nil {
		openImage.Close()
		mlog.Error("Serve:doConnect: Error reading sector 0, addr: %v, error: %v\n", u.addr.AddrPort(), err)
		return sr, err
	}

	// Parse the BPB to find the location of the first FAT sector.  A parse erro
	// Assumes this is a FAT volume ..  try to read the first byte of the first FAT
	// to get the media descriptor byte.  If this fails DOS isn't going to like the
	// file either, but we're going to continue anyway.

	testBpb, testBpbErr := bpb.New(firstSector[11:])

	mlog.Debug("Serve:doConnect: BPB, addr %v, filename: %s, BPB err msg: %v, BPB: %v\n", u.addr.AddrPort(), fn, testBpbErr, testBpb)

	mediaDescByte := byte(0)

	if testBpbErr == nil {

		if int64(testBpb.TotalSectors()*512) != openImage.Filesize {
			mlog.Warn("Serve:doConnect: BPB total sectors doesn't match filesize, addr: %v, image: %v, filesize: %v, BPB sectors*512: %v\n",
				u.addr.AddrPort(), openImage.Filename, openImage.Filesize, testBpb.TotalSectors()*512)
		}

		// There were no obvious errors when we parsed the BPB from the first
		// sector.  Compute the location of the first FAT sector, read it, and
		// extract the media descriptor byte.

		fatSectorLba := globals.Lba(testBpb.ReservedSectorCount)
		fatSector, err := openImage.ReadLbas(fatSectorLba, 1, 0)
		if err != nil {
			openImage.Close()
			mlog.Error("Serve:doConnect: Error reading media descriptor byte from FAT, addr: $v, error: %v\n", u.addr.AddrPort(), err)
			return sr, errors.New("error reading media descriptor byte from FAT")
		}
		mediaDescByte = fatSector[0]

	} else {

		// The BPB failed to parse.  This might be a DOS 1.x diskette.  If the
		// file size matches then proceed, otherwise return an error.

		if openImage.Filesize == 160*1024 || openImage.Filesize == 320*1024 {

			mlog.Debug("Serve:doConnect: Invalid BPB detected, but possibly a DOS 1.x diskette, addr: %v, error: %v.\n", u.addr.AddrPort(), testBpbErr)

			fatSectorLba := globals.Lba(1)
			fatSector, err := openImage.ReadLbas(fatSectorLba, 1, 0)
			if err != nil {
				openImage.Close()
				mlog.Error("Serve:doConnect: Bad BPB and error reading media descriptor byte from FAT, addr: %v, error: %v\n", u.addr.AddrPort(), err)
				return sr, errors.New("bad BPB and error reading media descriptor byte from FAT")
			}
			mediaDescByte = fatSector[0]

		} else {
			openImage.Close()
			mlog.Debug("Serve:doConnect: Bad BPB and file size doesn't match a 160K or 320K DOS 1.x disk, addr: %v, error: %v\n", u.addr.AddrPort(), fn)
			return sr, errors.New("bad BPB and file size doesn't match a 160K or 320K DOS 1.x disk")
		}
	}

	// Everything is working and we have everything we need.  Create a session_t
	// and send a response.

	// Set a random session ID.  0 is invalid, and right now I'm only assigning
	// session IDs above 32767.  (Maybe those lower ones will just be marked as
	// reserved.)
	//
	// While there might be multiple sessions active, doConnect is only ever
	// called from the processUdp thread so if we find a sessionID that is not
	// already in use there is no danger that it will become used before we
	// have a chance to update the sessions map and add this session.

	s := sessionId_t(0)

	for attempts := 0; attempts < 10; attempts++ {

		proposedSessionId := sessionId_t(rand.Intn(32768) + 32768)

		mapUpdateLock.RLock()
		_, exists := sessions[proposedSessionId]
		mapUpdateLock.RUnlock()

		if exists == false {
			s = proposedSessionId
			break
		}
	}

	if s == 0 {
		openImage.Close()
		mlog.Debug("Serve:doConnect: Could not get a unique session ID, addr: %v\n", u.addr.AddrPort())
		return sr, errors.New("Could not get a unique session ID")
	}

	sr = session_t{
		sessionId:  s,
		sequence:   u.cmd.Sequence,
		macAddr:    mac,
		netAddr:    *u.addr,
		protocol:   u.cmd.Version,
		openImage:  openImage,
		startTime:  time.Now(),
		lastActive: time.Now(),
		latency:    10000 * time.Millisecond,
		lastCmd:    u.cmd,
		clientType: clientType,
		blockSize:  blockSize,
		osString:   osString,
	}

	// Response message format:
	//
	// - 32 bit file size.
	// - 2 bytes of flags (0x00 is rw, 0x01 is read only)
	// - first 64 bytes of the boot sector
	// - media descriptor byte from FAT, or 0 if unable to determine.

	sizeBytes := make([]byte, 4)
	sizeBytes[0] = byte(openImage.Filesize & 0xff)
	sizeBytes[1] = byte((openImage.Filesize >> 8) & 0xff)
	sizeBytes[2] = byte((openImage.Filesize >> 16) & 0xff)
	sizeBytes[3] = byte((openImage.Filesize >> 24) & 0xff)

	flags := uint16(0x0)

	// Flag bits:
	//
	// Bit 0 - Is this file effectively read-only.
	// Bit 1 - Are session scoped writes enabled.

	// Possible values:
	//
	// 0x00 = File is effectively read-write.  (File is RW directly or it has a goback-style journal.)
	// 0x01 = File is effectively read-only.   (File is RO and no goback-style journal or session scoped writes.)
	// 0x02 = Session scoped writes, don't care if the underlying file is RO or RW.
	// 0x03 = Session scoped writes, don't care if the underlying file is RO or RW.
	//        Note you will not see this because if session scoped is set we don't turn RO.
	//
	// We never have both a go-back style journal and session scoped writes at the same time.

	// Image is read-only and never going to have a journal - warn the user that it is readonly.
	if (openImage.Readonly == true) && (openImage.JournalType == image.NoJournal) {
		flags |= 0x01
	}

	// Image has a session scoped journal so writes will not persist - warn them.
	if openImage.JournalType == image.SessionScoped {
		flags |= 0x02
	}

	partOfFirstSector := firstSector[0:64]

	respBytes := append(sizeBytes, byte(flags&0xff))
	respBytes = append(respBytes, byte((flags>>8)&0xff))
	respBytes = append(respBytes, partOfFirstSector...)
	respBytes = append(respBytes, mediaDescByte)

	resp := command{Version: sr.protocol, Session: s, Sequence: u.cmd.Sequence, Operation: op_connect, CmdExt: u.cmd.CmdExt, Result: 0}
	sendResp(u.conn, u.addr, resp, respBytes)

	mlog.Info("Serve:doConnect: New Session, addr: %v, session: %v\n", u.addr.AddrPort(), sr)

	var attributes string
	if openImage.Readonly == true {
		attributes = "(RO)"
	} else {
		attributes = "(RW)"
	}

	attributes = attributes + " Journal type: " + openImage.JournalType.String()

	infoMsgChannelTs <- fmt.Sprintf("New Session: %d, Addr: %s:%d, Protocol: %d, Image: %s %s\n",
		s, u.addr.IP.To4(), u.addr.Port, sr.protocol, fn, attributes)

	return sr, nil
}

func doDisconnect(u udpCmd, s *session_t) {

	// Setup most of the response packet and default to a bad return code.
	resp := command{
		Version:   s.protocol,
		Session:   s.sessionId,
		Sequence:  u.cmd.Sequence,
		Operation: op_disconnect,
		CmdExt:    u.cmd.CmdExt,
		Result:    1,
	}

	// We don't care if this is a retry; if we are seeing it then it's the first
	// time here and we are going to execute it.  If we had seen it already then
	// the session would be dead and you wouldn't be here.
	if _, err := s.updateSequence(u.cmd); err != nil {
		sendResp(u.conn, u.addr, resp, []byte("Bad sequence"))
		return
	}

	mlog.Info("Serve:doDisconnect End session, addr: %v, session: %v\n", u.addr.AddrPort(), s.sessionId)

	resp.Result = 0
	sendResp(u.conn, u.addr, resp, []byte("OK"))

	stats.disconnects++

	retireSession(s.sessionId, "client disconnected")
}

func doReadAhead(u udpCmd, s *session_t, rs globals.Lba, c uint16 ) {

	// Setup most of the response packet and default to a bad return code.
	//
	// Sequence is not enforced on the client side because these arrive
	// asynchronously.  The client side avoids trouble by invaliding the
	// readahead cache when a write starts or when a different unit gets
	// accessed.
	resp := command{
		Version:   s.protocol,
		Session:   s.sessionId,
		Sequence:  u.cmd.Sequence,
		StartSec:  rs,
		SecCount:  c,
		Operation: op_readahead,
		CmdExt:    u.cmd.CmdExt,
		Result:    0,
	}

	payload, err := s.openImage.ReadLbas(rs, c, int(s.sessionId))
	if err != nil {
	  return
	}

	sendResp(u.conn, u.addr, resp, payload)
}

func doRead(u udpCmd, s *session_t) {

	// Setup most of the response packet and default to a bad return code.
	resp := command{
		Version:   s.protocol,
		Session:   s.sessionId,
		Sequence:  u.cmd.Sequence,
		StartSec:  u.cmd.StartSec,
		SecCount:  u.cmd.SecCount,
		Operation: op_read,
		CmdExt:    u.cmd.CmdExt,
		Result:    1,
	}

	retryDetected, err := s.updateSequence(u.cmd)
	if err != nil {
		sendResp(u.conn, u.addr, resp, []byte("Bad sequence"))
		return
	}

	if (u.cmd.SecCount < 1) || (u.cmd.SecCount > 2) {
		mlog.Warn("Serve:doRead: Bad sector count, addr: %v, session: %v, cmd: %v\n", u.addr.AddrPort(), s.sessionId, u.cmd)
		sendResp(u.conn, u.addr, resp, []byte("Read error: bad sector count"))
		return
	}

	// Reading past the end of the file?
	if (int64(u.cmd.StartSec+globals.Lba(u.cmd.SecCount)) * 512) > s.openImage.Filesize {
		mlog.Warn("Serve:doRead: Attempted read past end of file, addr: %v, session: %v, fileSize: %d, start sector: %d, sector count: %d\n",
			u.addr.AddrPort(), s.sessionId, s.openImage.Filesize, u.cmd.StartSec, u.cmd.SecCount)
		sendResp(u.conn, u.addr, resp, []byte("Attempted read past end of image"))
		return
	}

	payload, err := s.openImage.ReadLbas(u.cmd.StartSec, u.cmd.SecCount, int(s.sessionId))
	if err != nil {
		sendResp(u.conn, u.addr, resp, []byte("Read error"))
		return
	}

	// Even if it's a retry it's ok to re-execute it.  We just want to know
	// that it is a retry for the stats.
	if retryDetected == false {
		s.blocksRead += int(u.cmd.SecCount)
		stats.blocksRead += int(u.cmd.SecCount)
	} else {
		stats.retriesRead++
	}

	if f_TortureTest == true {
		// Delay the outgoing response, just a little bit.
		lottery := rand.Intn(2000)
		if lottery == 0 {
			time.Sleep(time.Duration(rand.Intn(5)) * time.Second)
		}
	}

	resp.Result = 0

	if f_TortureTest == true {
		// Meh, throw a false error
		lottery := rand.Intn(2000)
		if lottery == 0 {
			resp.Result = 1
		}
	}


	// Read-ahead processing
	//
	// Check the payload for the hints needed to do read-ahead.  If there is no
	// payload, the payload is not six bytes long, or the payload bytes are zero
	// then ignore it.
	//
	// Versions 1 and 2 of the protocol don't support read-ahead.  They could 
	// have because the hint is just in the optional payload, but we'll just
	// keep things simple.
	//
	// For version 3:
	//
	// * The first two bytes are the maximum number of read-ahead blocks to send.
	// * The next two bytes are the remaining blocks in the current read being
	//   processed by the client.
	// * The last two bytes are a delay in milliseconds to use between sending
	//   read-ahead packets.

	if retryDetected == false && len(u.optData) == 6 {

		maxReadAheadBlocks := uint16(u.optData[0]) + uint16(u.optData[1])*256
		remainingBlocks := uint16(u.optData[2]) + uint16(u.optData[3])*256
		msDelay := int(u.optData[4]) + int(u.optData[5])*256

		if maxReadAheadBlocks != 0 || remainingBlocks != 0 {

			// Don't count the current read command blocks in the read-ahead blocks that we might send.
		  readAheadBlocks := remainingBlocks - uint16(u.cmd.SecCount) 

		  mlog.Debug("Serve:doRead: Readahead requested, max readahead blocks: %v, cmd size: %v\n", maxReadAheadBlocks, remainingBlocks )

		  if readAheadBlocks > maxReadAheadBlocks {
			  readAheadBlocks = maxReadAheadBlocks
		  }

		  lba := u.cmd.StartSec + globals.Lba(u.cmd.SecCount)

		  mlog.Debug("Serve:doRead: Readahead for session %v: start: %v, blocks: %v\n", s.sessionId, lba, readAheadBlocks)

		  for ; readAheadBlocks > 0 ; {

			  chunkSize := readAheadBlocks
			  if chunkSize > 2 {
				  chunkSize = 2
			  }

			  doReadAhead(u, s, lba, chunkSize)

			  if msDelay > 0 {
				  time.Sleep(time.Duration(msDelay) * time.Millisecond)
			  }

			  lba += globals.Lba(chunkSize)
			  readAheadBlocks -= chunkSize

		  }

		}

	}

	// Send the response *after* filling in the read-ahead cache so that the
	// client is guaranteed not to stumble.  This increases latency slightly
	// for when there are read-ahead blocks to process, but the overall
	// throughput will increase.

	// Fixme: Probably should send out a few read-ahead packets first, then
	// what they actually requested, and then continue with read-ahead packets.
	sendResp(u.conn, u.addr, resp, payload)

}

func doWrite(u udpCmd, s *session_t) {

	// Setup most of the response packet and default to a bad return code.
	resp := command{
		Version:   s.protocol,
		Session:   s.sessionId,
		Sequence:  u.cmd.Sequence,
		StartSec:  u.cmd.StartSec,
		SecCount:  u.cmd.SecCount,
		Operation: u.cmd.Operation,
		CmdExt:    u.cmd.CmdExt,
		Result:    1,
	}

	retryDetected, err := s.updateSequence(u.cmd)
	if err != nil {
		sendResp(u.conn, u.addr, resp, []byte("Bad sequence"))
		return
	}

	// Basic checks for this command

	if u.cmd.SecCount < 1 {
		mlog.Warn("Serve:doWrite: Bad sector count on command, addr: %v, sector count: %v\n", u.addr.AddrPort(), u.cmd.SecCount)
		sendResp(u.conn, u.addr, resp, []byte("Bad sector count"))
		return
	}

	if len(u.optData) != (512 * int(u.cmd.SecCount)) {
		mlog.Warn("Serve:doWrite: Payload doesn't match write length, addr: %v, session: %v, payload len: %v, sector count: %v\n",
			u.addr.AddrPort(), s.sessionId, len(u.optData), u.cmd.SecCount)
		sendResp(u.conn, u.addr, resp, []byte("Bad write payload"))
		return
	}

	// Writing past the end of the file?
	if (int64(u.cmd.StartSec+globals.Lba(u.cmd.SecCount)) * 512) > s.openImage.Filesize {
		mlog.Warn("Serve:doWrite: Attempted write past end of file, addr: %v, session: %v, fileSize: %d, start sector: %d, sector count: %d\n",
			u.addr.AddrPort(), s.sessionId, s.openImage.Filesize, u.cmd.StartSec, u.cmd.SecCount)
		sendResp(u.conn, u.addr, resp, []byte("Attempted write past end of image"))
		return
	}

	if (s.protocol == 3) && (u.cmd.Operation == op_write_verify) {

		// Only care about the low order byte.  We might need the other byte for
		// something else and one byte is overkill already.
		if (u.cmd.CmdExt & 0xff) > 2 {
			mlog.Warn("Serve:doWrite: Bad check algorithm specified for write_verify, addr: %v, session: %v, cmdExt: %v\n",
				u.addr.AddrPort(), s.sessionId, u.cmd.CmdExt)
			sendResp(u.conn, u.addr, resp, []byte("Unknown CRC algorithm for write-verify"))
			return
		}
	}


	// Exit early if writes are not allowed: Read-only file with no journal or session scoped writes.

	if (s.openImage.Readonly == true) && (s.openImage.JournalType == image.NoJournal) {
		mlog.Debug("Serve:doWrite: Write protect error, addr: %v, session: %v\n", u.addr.AddrPort(), s.sessionId)
		sendResp(u.conn, u.addr, resp, []byte("Write protect"))
		return
	}

	if (s.openImage.JournalType == image.SessionScoped) && (f_SessionScopedWritesLBALimit > 0) && (s.openImage.Journal.MapSize() > f_SessionScopedWritesLBALimit) {
		mlog.Debug("Serve:doWrite: Session scoped session write limit hit, addr: %v, session: %v, map size: %v\n",
			u.addr.AddrPort(), s.sessionId, s.openImage.Journal.MapSize())
		sendResp(u.conn, u.addr, resp, []byte("Write error: session LBA limit"))
		return
	}

	// If we are using session scoped writes then we open a journal file on first
	// touch.  If this device is using goback then the journal was already opened
	// when the device was opened and there is nothing to do here.

	if (s.openImage.JournalType == image.SessionScoped) && (s.openImage.IsJournalOpen() == false) {

		fn := fmt.Sprintf("%d", s.sessionId)

		if err := s.openImage.OpenSessionJournal(fn, f_SessionScopedWritesDir); err != nil {
			mlog.Error("Serve:doWrite: Unable to open session journal, addr: %v, session: %d, error: %v\n", u.addr.AddrPort(), s.sessionId, err)
			sendResp(u.conn, u.addr, resp, []byte("Write error: journal creation"))
			return
		}

		infoMsgChannelTs <- fmt.Sprintf("Session journal file created for %d.\n", s.sessionId)

	}

	// If this is a retry of a previous attempt we should not need to execute it
	// again, but the payload might have changed.  So execute the command anyway,
	// as from the client's point of view the last one in wins.

	if err := s.openImage.WriteLbas(u.cmd.StartSec, u.cmd.SecCount, u.optData, int(s.sessionId)); err != nil {
		sendResp(u.conn, u.addr, resp, []byte(err.Error()))
		return
	}

	returnPayload := []byte{}

	// If this is op_write_verify then reread the requested data from disk and send it back.
	if u.cmd.Operation == op_write_verify {

		readBack, err := s.openImage.ReadLbas(u.cmd.StartSec, u.cmd.SecCount, int(s.sessionId))

		if err != nil {
			mlog.Error("Serve:doWrite: Unable to read back what we just wrote, addr: %v, session: %v, error: %v\n", u.addr.AddrPort(), s.sessionId, err)
			sendResp(u.conn, u.addr, resp, []byte("Write verify error: readback"))
			return
		}

		if bytes.Equal(u.optData, readBack) == false {
			mlog.Error("Serve:doWrite: Write-verify miscompare!, addr: %v, session: %v\n", u.addr.AddrPort(), s.sessionId)
			sendResp(u.conn, u.addr, resp, []byte("Write verify error: miscompare"))
			return
		}

		if s.protocol < 3 {
			// Send the readback data to them in the payload.
			returnPayload = readBack
		} else {
			// What algorithm did they tell us to use? (We already checked it for a valid range above.)
			algorithm := u.cmd.CmdExt & 0xff
			switch algorithm {
			case 1:
				crc := ComputeCRC( readBack )
				returnPayload = append(returnPayload, uint8(crc & 0xff), uint8(crc >> 8), 0, 0 )
				mlog.Debug("Serve:doWrite: Computed CRC (algoritm %v) on %v bytes.\n", algorithm, len(readBack))
			case 2:
				f32 := ComputeFletcher32( readBack )
				returnPayload = append(returnPayload, uint8(f32 & 0xff), uint8(f32 >> 8), uint8(f32 >> 16), uint8(f32 >> 24) )
				mlog.Debug("Serve:doWrite: Computed Fletcher-32 (algoritm %v) on %v bytes.\n", algorithm, len(readBack))
			}
		}


		if f_TortureTest == true {
			lottery := rand.Intn(1000)
			if lottery == 0 {
				mlog.Debug("Serve:doWrite: Error injected on a write-verify packet, addr: %v, session: %v\n", u.addr.AddrPort(), s.sessionId)
				returnPayload[rand.Intn(len(returnPayload))] = byte(rand.Intn(256))
			}
		}

	}

	if retryDetected == false {
		s.blocksWritten += int(u.cmd.SecCount)
		stats.blocksWritten += int(u.cmd.SecCount)
	} else {
		stats.retriesWrite++
	}

	if f_TortureTest == true {
		// Delay the outgoing response, just a little bit.
		lottery := rand.Intn(2000)
		if lottery == 0 {
			time.Sleep(time.Duration(rand.Intn(5)) * time.Second)
		}
	}

	resp.Result = 0

	if f_TortureTest == true {
		// Meh, throw a false error
		lottery := rand.Intn(2000)
		if lottery == 0 {
			resp.Result = 1
		}
	}

	sendResp(u.conn, u.addr, resp, returnPayload)

}

func doMarkCheckpoint(u udpCmd, s *session_t) {

	// Setup most of the response packet and default to a bad return code.
	resp := command{
		Version:   s.protocol,
		Session:   s.sessionId,
		Sequence:  u.cmd.Sequence,
		Operation: op_mark_checkpoint,
		CmdExt:    u.cmd.CmdExt,
		Result:    1,
	}

	if _, err := s.updateSequence(u.cmd); err != nil {
		sendResp(u.conn, u.addr, resp, []byte("Bad sequence"))
		return
	}

	// Invalidate the cached list of checkpoints.
	s.checkpoints = nil

	// Execute this even on a retry.  At worst case you get an extra checkpoint marker.

	if err := s.openImage.Journal.MarkCheckpoint(string(u.optData[:])); err != nil {
		mlog.Warn("Serve:doMarkCheckpoint: Mark checkpoint failed, addr: %v, session: %v, error: %v.\n", u.addr.AddrPort(), s.sessionId, err)
		sendResp(u.conn, u.addr, resp, []byte(err.Error()))
		return
	}

	mlog.Info("Serve:doMarkCheckpoint: addr: %v, session: %v, tag: %v\n", u.addr.AddrPort(), s.sessionId, string(u.optData))
	infoMsgChannelTs <- fmt.Sprintf("MarkCheckpoint: %v %s\n", s.sessionId, string(u.optData))

	resp.Result = 0
	sendResp(u.conn, u.addr, resp, []byte("OK"))
}

func doGotoCheckpoint(u udpCmd, s *session_t) error {

	// Setup most of the response packet and default to a bad return code.
	resp := command{
		Version:   s.protocol,
		Session:   s.sessionId,
		Sequence:  u.cmd.Sequence,
		Operation: op_goto_checkpoint,
		CmdExt:    u.cmd.CmdExt,
		Result:    1,
	}

	retryDetected, err := s.updateSequence(u.cmd)
	if err != nil {
		sendResp(u.conn, u.addr, resp, []byte("Bad sequence"))
		return errors.New("bad sequence")
	}

	// Invalidate the cached list of checkpoints.
	s.checkpoints = nil

	// If this operation fails the user should get the error string.
	// If it succeeds we also give additional information to them.
	var userMsg string

	// Do not re-execute this command if we think it is a retry; it
	// is too risky.  The client will get a visible user message
	// saying we refused to do it, and they can retry it again.

	if retryDetected == true {
		mlog.Debug("Serve:doGotoCheckpoint: Retry detected, not executing it, addr: %v, session: %d, cmd: %v, payload: %v\n",
			u.addr.AddrPort(), s.sessionId, u.cmd, u.optData)
		userMsg = "Packet loss and retry detected, command not executed again."
		sendResp(u.conn, u.addr, resp, []byte(userMsg))
		return errors.New("retry detected")
	}

	start := time.Now()
	userMsg, err = s.openImage.Journal.GotoCheckpoint(u.optData)
	if err != nil {
		if e, ok := err.(*globals.FatalError); ok {
			mlog.Error("Serve:doGotoCheckpoint: Fatal error, killing session, addr: %v, session: %d, error: %v\n", u.addr.AddrPort(), s.sessionId, err)
			sendResp(u.conn, u.addr, resp, []byte(e.Error()+", terminating session"))
		} else {
			mlog.Warn("Serve:doGotoCheckpoint: Non-fatal error, addr: %v, session %d, error: %v\n", u.addr.AddrPort(), s.sessionId, err)
			sendResp(u.conn, u.addr, resp, []byte(err.Error()))
		}
		return err
	}
	elapsed := time.Since(start).Round(time.Millisecond)

	mlog.Info("Serve:doGotoCheckpoint: Result: addr: %v, session: %v, userMsg: %v\n", u.addr.AddrPort(), s.sessionId, userMsg)
	infoMsgChannelTs <- fmt.Sprintf("GotoCheckpoint: Session: %v, msg: %v\n", s.sessionId, userMsg)

	if elapsed.Milliseconds() > 8000 {
		mlog.Warn("Serve:doGotoCheckpoint: Goto took %v to execute; the journal might be very large.  Addr: %v, session: %d\n", elapsed, u.addr.AddrPort(), s.sessionId)
		infoMsgChannelTs <- fmt.Sprintf("** WARNING **: Journal for image %s is getting large; consider doing file maintenance to shrink it.\n", s.openImage.Filename)
	}

	resp.Result = 0
	sendResp(u.conn, u.addr, resp, []byte(userMsg))

	return nil
}

func doListCheckpoints(u udpCmd, s *session_t) {

	// Setup most of the response packet and default to a bad return code.
	resp := command{
		Version:   s.protocol,
		Session:   s.sessionId,
		Sequence:  u.cmd.Sequence,
		Operation: op_list_checkpoints,
		Result:    1,
		CmdExt:    u.cmd.CmdExt,
	}

	if _, err := s.updateSequence(u.cmd); err != nil {
		sendResp(u.conn, u.addr, resp, []byte("Bad sequence"))
		return
	}

	// Defaults for protocol version 2
	startLine := 0
	maxBytes := 1024

	if s.protocol >= 3 {
		startLine = int(u.optData[0]) + int(u.optData[1])*256
		maxBytes = int(u.optData[2]) + int(u.optData[3])*256

		if (maxBytes < 200 ) {
			mlog.Warn("Serve:doListCheckpoints: Client buffer too small, addr: %v, session: %d, size: %v\n", u.addr.AddrPort(), s.sessionId, maxBytes)
			sendResp(u.conn, u.addr, resp, []byte("Buffer too small"))
			return
		}

		// Reduce the output buffer by 3 so we can send the "more data" flag
		// and the number of lines sent.
		maxBytes = maxBytes - 3

	}

	mlog.Debug("Serve:doListCheckpoints: addr: %v, session: %d, startLine: %v, maxBytes: %v\n", u.addr.AddrPort(), s.sessionId, startLine, maxBytes)

	if s.checkpoints == nil || len(s.checkpoints) == 0 {
		l, err := s.openImage.Journal.ListCheckpoints()
		s.checkpoints = l
		if err != nil {
			mlog.Warn("Serve:doListCheckpoints: Failed, addr: %v, session: %d, error: %v\n", u.addr.AddrPort(), s.sessionId, err)
			sendResp(u.conn, u.addr, resp, []byte(err.Error()))
			return
		}
	}

	payload := make([]byte, 0, maxBytes)

	exceededPayload := byte(0)

	linesSent := 0

	for i, v := range s.checkpoints {
	  if i >= startLine {
			if len(payload)+len(v)+2 < maxBytes {
				payload = append(payload, []byte(v)...)
				payload = append(payload, []byte("\n")...)
	      linesSent++
			} else {
				exceededPayload = 1
				break
			}
		}
	}

	if s.protocol >= 3 {
		// Insert the number of lines we sent.  This will wind up as bytes 2 and 3 of the payload.
		payload = append([]byte{byte(linesSent & 0xff), byte((linesSent >> 8) & 0xff)}, payload...)

		// Insert the "more data" flag as the first byte of the payload.
		payload = append([]byte{exceededPayload}, payload...)
	}

	mlog.Debug("Serve:doListCheckpoints: Sending list, addr: %v, session: %d, bytes: %v\n", u.addr.AddrPort(), s.sessionId, len(payload))

	resp.Result = 0
	sendResp(u.conn, u.addr, resp, payload)
}
