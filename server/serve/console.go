/*

   mTCP NetDrive Server (console.go)
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


   Description: Text user interface for the NetDrive server.

*/

package serve


/* This code handles the text user interface for the server.  The message
   handling and screen drawing parts are generic and might be able to
	 be reused in other projects.

   The server code initializes the console and then sends messages to be
	 displayed using a channel.  This code stores the messages (up to a
	 reasonable limit) allowing them to be redisplayed if the console changes
	 size.  This code also has some basic keyboard handling, allowing the
	 user to get a server status report, disconnect a client, and set a few
	 variables.

	 If the user chooses "headless" mode the console is not initialized.  The
	 server will still send log messages to be displayed, but an early exit
	 will keep those messages from going anywhere.  It's overhead but not
	 enough to worry about.

	 Eventually this code should be modified to run as a true background daemon.

*/


import (
	"fmt"
	"strings"
	"time"

	"brutman.com/brutman/netdrive/globals"
	"brutman.com/brutman/netdrive/mlog"

	"github.com/gdamore/tcell/v2"
)

// tcell related functions and variables

var screen tcell.Screen

var defaultStyle = tcell.StyleDefault.Background(tcell.ColorBlack).Foreground(tcell.ColorWhite)
var titleStyle = tcell.StyleDefault.Background(tcell.ColorBlue).Foreground(tcell.ColorWhite)

var backScroll = 0

var max_x, max_y, infoLines int

func initConsole() (chan tcell.Event, error) {

	tcell.SetEncodingFallback(tcell.EncodingFallbackASCII)

	var err error
	screen, err = tcell.NewScreen()
	if err != nil {
		return nil, fmt.Errorf("Error initializing tcell: %+v\n", err)
	}
	if err := screen.Init(); err != nil {
		return nil, fmt.Errorf("Error initializing tcell screen: %+v\n", err)
	}
	screen.SetStyle(defaultStyle)
	screen.Clear()

	max_x, max_y = screen.Size()
	infoLines = max_y - 4

	// e is the event channel that the server will read and process in its main loop.
	// q is the quit channel that tells screen.ChannelEvents to shut down and return.
	e := make(chan tcell.Event)
	q := make(<-chan struct{})

	go screen.ChannelEvents(e, q)

	return e, nil
}

// drawScreen takes a slice of messages and writes them to the screen.
// We send it a string slice instead of using infoMsgs directly because infoMsgs
// might be altered while drawString is updating the screen.  Having a copy of
// the string slice protects us and lets the garbage collector deal with the
// copy after we are done with it.
func drawScreen(msgs []string) {

	// If there is no screen to work or the message log hasn't been created yet
	// then there is nothing to do.
	if screen == nil || msgs == nil {
		return
	}

	// Refuse to do less than a certain screen size to avoid crashes.
	if max_x < 10 || max_y < 10 {
		return
	}

	drawAt(0, 0, titleStyle, true, "mTCP NetDrive server by M Brutman, Build: "+globals.BuildDate)

	for i := 0; i < max_x; i++ {
		screen.SetContent(i, 1, tcell.RuneHLine, nil, defaultStyle)
	}
	for i := 0; i < max_x; i++ {
		screen.SetContent(i, max_y-2, tcell.RuneHLine, nil, defaultStyle)
	}

	// infoLines is one screen.  If you set a starting msg line that is
	// further back than you have messages, limit it to the number of
	// remaining messages.

	if backScroll >= len(msgs)-infoLines {
		backScroll = len(msgs) - infoLines
	}

	// Likewise, don't let backScroll go negative
	if backScroll < 0 {
		backScroll = 0
	}

	// Determine the first message to print.
	index := len(msgs) - infoLines - backScroll
	if index < 0 {
		index = 0
	}

	x := 0
	y := 2

	// Stop when you fill the info area or run out of msg lines
	for i := 0; (y < max_y-2) && (index+i < len(msgs)); i++ {
		drawAt(x, y, defaultStyle, true, msgs[index+i])
		x = 0
		y++
	}

	screen.Show()
}

func drawAt(x, y int, style tcell.Style, fillLine bool, str string) {

	// Draw the string
	for _, j := range str {
		screen.SetContent(x, y, j, nil, style)
		x++
	}

	// Clear the rest of the line if requested.
	if fillLine == true {
		for ; x < max_x; x++ {
			screen.SetContent(x, y, ' ', nil, style)
		}
	}

}

func clearInputArea() {
	for i := 0; i < max_x; i++ {
		screen.SetContent(i, max_y-1, ' ', nil, defaultStyle)
	}
}

// Informational message handling
//
// Any of the goroutines can send a message to add to the screen by sending
// a string to the infoMsgChannel.  We break the string into individual
// lines and add it to the infoMsgs buffer.  (One line per buffer makes
// the indexing easier when drawing the screen.)  If the number of messages
// exceeds 10,000 we remove the oldest 1,000.
//
// * And empty string results in a blank line being added.
// * If the caller forgets the newline char that's ok.
// * If they ended with a newline Split gives us a slice where the last value
//   is empty.  Don't add that empty string.
//
// Note: to keep things simple we always have this running in the background,
// even if we are not putting anything up on the screen.  In that case infoMsgs
// never gets created.

var infoMsgs []string // Slice of messages for this session.

var infoMsgChannel chan string       // Channel for adding new messages.
var infoMsgChannelTs chan string     // Channel for adding new messages.
var infoMsgChannelExit chan struct{} // Signal channel to end message processing.
var infoMsgChannelDone chan struct{} // Signal channel that indicates we are done

func addMsg() {

	done := false
	for done == false {

		select {

		case <-infoMsgChannelExit:
			done = true

		case str := <-infoMsgChannel:

			// Early exit; if we have no message log then there is no point to
			// processing messages.  (This is true in Headless mode.)
			if infoMsgs == nil {
				break
			}

			// If the info buffer gets too big trim it.  This should be safe because
			// we are serialized here by the channel and the drawScreen function gets
			// a slice passed in; that slice (the current info buffer) won't be GC'ed
			// until after drawScreen is done.

			if len(infoMsgs) > 11000 {
				shorter := make([]string, 0, 1000)
				for i := 1000; i < len(infoMsgs); i++ {
					shorter = append(shorter, infoMsgs[i])
				}
				infoMsgs = shorter
			}

			if len(str) == 0 {
				infoMsgs = append(infoMsgs, "")
			} else {
				s2 := strings.Split(str, "\n")
				// Did they forget the newline?
				if len(s2) == 1 {
					infoMsgs = append(infoMsgs, str)
				} else {
					if len(s2[len(s2)-1]) == 0 {
						s2 = s2[0 : len(s2)-1]
					}
					infoMsgs = append(infoMsgs, s2...)
				}
			}

		} // end select

		if infoMsgs != nil {
			drawScreen(infoMsgs)
			screen.Show()
		}

	}

	// The main thread should block on this channel before tearing down the tcell
	// screen so that we know we're not trying to asynchronously use the tcell
	// screen after it is gone.
	close(infoMsgChannelDone)

	// Just keep reading new messages and throwing them away in case there are
	// laggard goroutines out there.
	for range infoMsgChannel {
	}

}

// User input handling

var cmdInput []byte
var cmdInputLen int

var evLoopDone bool
var quitCount int

// Called from the server in its main event processing loop.
func handleTcellEvent(e tcell.Event) {
	switch e := e.(type) {
	case *tcell.EventResize:
		screen.Clear()
		drawScreen(infoMsgs)
		screen.Sync()
		max_x, max_y = screen.Size()
		infoLines = max_y - 4
	case *tcell.EventKey:
		consoleProcessKey(e)
	} // end switch
}

func consoleProcessKey(ev *tcell.EventKey) {

	if ev.Key() == tcell.KeyCtrlC {

		quitCount++
		if quitCount == 1 {
			infoMsgChannel <- "Press Ctrl-C one more time to confirm quitting."
		} else {
			evLoopDone = true
		}

	} else {

		quitCount = 0

		if ev.Key() == tcell.KeyCtrlL {

			screen.Clear()
			drawScreen(infoMsgs)
			screen.Sync()

		} else if ev.Key() == tcell.KeyESC {

			cmdInput = make([]byte, 0, 80)
			cmdInputLen = 0
			clearInputArea()

		} else if ev.Key() == tcell.KeyEnter {

			consoleProcessEnter()

		} else if ev.Key() == tcell.KeyBS || ev.Key() == tcell.KeyDEL {

			if cmdInputLen > 0 {
				cmdInputLen--
				cmdInput = cmdInput[0:cmdInputLen]
				screen.SetContent(cmdInputLen, max_y-1, ' ', nil, defaultStyle)
			}

		} else if ev.Key() == tcell.KeyPgUp {

			backScroll = backScroll + infoLines
			drawScreen(infoMsgs)

		} else if ev.Key() == tcell.KeyPgDn {

			backScroll = backScroll - infoLines
			drawScreen(infoMsgs)

		} else if ev.Key() == tcell.KeyRune {

			screen.SetContent(cmdInputLen, max_y-1, ev.Rune(), nil, defaultStyle)
			cmdInput = append(cmdInput, byte(ev.Rune()))
			cmdInputLen++

		}

	} // end keys not Ctrl-C

}

func consoleProcessEnter() {

	backScroll = 0

	if len(cmdInput) == 0 {
		drawScreen(infoMsgs)
	} else {

		cmdStr := string(cmdInput[:])
		infoMsgChannel <- fmt.Sprintf("%v > %v", time.Now().Format(timestampFormat), cmdStr)
		mlog.Info("Console command: " + cmdStr + "\n")

		switch strings.ToLower(strings.SplitN(cmdStr, " ", 2)[0]) {
		case "help":
			consoleHelp()
		case "kill":
			consoleKill(cmdStr)
		case "quit":
			evLoopDone = true
		case "status":
			consoleStatus()
		case "set":
			consoleSet(cmdStr)
		}

		cmdInputLen = 0
		cmdInput = make([]byte, 0, 80)
		clearInputArea()
	}

}

func consoleStatus() {

	mapUpdateLock.RLock()

	infoMsgChannel <- stats.String()

	infoMsgChannel <- fmt.Sprintf("\nServer flags: Max session length: %v, Session timeout: %v, Max active sessions: %v\n",
		f_SessionMaxTime, f_SessionTimeout, f_MaxActiveSessions)

	infoMsgChannel <- "\nClients:"
	if len(sessions) == 0 {
		infoMsgChannel <- "  No connected clients"
	} else {
		infoMsgChannel <- ""
		for _, v := range sessions {
			s := v.session

			var attributes string
			if s.openImage.Readonly == true {
				attributes = "(RO)"
			} else {
				attributes = "(RW)"
			}

			attributes = attributes + " Journal type: " + s.openImage.JournalType.String()

			infoMsgChannel <- fmt.Sprintf("Session: %5d, %s:%d, Protocol: %d, Image: %s %s\n         Started: %s, Last seen: %s, OS Ver: %v\n         Blocks (Read: %d, Written: %d), Retries: %d, Latency: %d",
				s.sessionId, s.netAddr.IP.To4(), s.netAddr.Port, s.protocol, s.openImage.Filename, attributes,
				s.startTime.Format(timestampFormat), s.lastActive.Format(timestampFormat), s.osString,
				s.blocksRead, s.blocksWritten, s.retries, s.latency.Milliseconds())
		}
	}
	infoMsgChannel <- ""

	mapUpdateLock.RUnlock()
}

func consoleHelp() {
	infoMsgChannel <- `
Commands:

  Help       Display this help text
  Kill <n>   Kill session number n or ALL to kill all sessions
  Quit       Quit the program
  Set        Set options
  Status     Show connected clients and open files

  Esc        Clear the command input area
  Ctrl-C     Quit (use twice in a row)
  Ctrl-L     Refresh the screen

`
}

func consoleKill(cmd string) {

	var targetSessionString string
	fmt.Sscanf(cmd, "kill %v", &targetSessionString)

	if strings.ToLower(targetSessionString) == "all" {

		infoMsgChannel <- "Killing all sessions"

		mapUpdateLock.RLock()
		deadPool := make([]sessionId_t, 0, 20)
		for _, v := range sessions {
			deadPool = append(deadPool, v.session.sessionId)
		}
		mapUpdateLock.RUnlock()

		for _, v := range deadPool {
			mlog.Info(fmt.Sprintf("Serve: Killing session: %v\n", v))
			if err := retireSession(v, "kill all command"); err != nil {
				mlog.Warn(fmt.Sprintf("Serve: Tried to kill an invalid session: %v\n", v))
			}
		}

	} else {

		var targetSessionId sessionId_t
		fmt.Sscanf(cmd, "kill %v", &targetSessionId)

		if targetSessionId == 0 {
			infoMsgChannel <- "Kill requires a session number"
			return
		}

		if err := retireSession(targetSessionId, "by SYSOP decree!"); err != nil {
			infoMsgChannel <- "Invalid session number"
		}

	}

}

func consoleSet(cmd string) {

	tokens := strings.SplitN(cmd, " ", -1)

	if len(tokens) < 3 {
		infoMsgChannel <- `
Set commands:

  set timeout <n>                 Set the session timeout in minutes
  set max_time <n>                Set the maximum session time
  set max_active_sessions <n>     Set the maximum number of active sessions
  set ssw_unique_lbas_limit <n>   Set the max unique number of lbas while using session scoped writes
  
`
		return
	}

	switch strings.ToLower(tokens[1]) {

	case "timeout":
		var newTimeout int
		fmt.Sscanf(tokens[2], "%d", &newTimeout)
		if newTimeout > -1 {
			f_SessionTimeout = newTimeout
			infoMsgChannel <- fmt.Sprintf("Session timeout set to %v", f_SessionTimeout)
		}
	case "max_time":
		var newMaxTime int
		fmt.Sscanf(tokens[2], "%d", &newMaxTime)
		if newMaxTime > -1 {
			f_SessionMaxTime = newMaxTime
			infoMsgChannel <- fmt.Sprintf("Session max time set to %v", f_SessionMaxTime)
		}
	case "max_active_sessions":
		var newMaxSessions int
		fmt.Sscanf(tokens[2], "%d", &newMaxSessions)
		if newMaxSessions > 0 {
			f_MaxActiveSessions = newMaxSessions
			infoMsgChannel <- fmt.Sprintf("Max active sessions set to %v", f_MaxActiveSessions)
		}
	case "ssw_unique_lbas_limit":
		var newLbas int
		fmt.Sscanf(tokens[2], "%d", &newLbas)
		if newLbas > -1 {
			f_SessionScopedWritesLBALimit = newLbas
			infoMsgChannel <- fmt.Sprintf("Session scoped writes unique LBAs limit set to %v", f_SessionScopedWritesLBALimit)
		}
	}

}
