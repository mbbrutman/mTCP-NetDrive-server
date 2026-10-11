/*

   mTCP NetDrive Server (netdrive.go)
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


   Description: NetDrive server main entry point.

*/

/*
NetDrive serves images to clients and provides tools for creating and
maintaining images.

Usage:

  netdrive [common flags] command [command flags] <command parms ...>

Commands are:

  create         Create a floppy or hard disk image
  image          Perform image maintenance
  serve          Run the disk server

Common flags for all commands:

  -log_file  <filename>     Set a filename to log to, default is no logging
  -log_level <level>        Log level (debug|info|warn|error), default is info

Use "netdrive create help", "netdrive serve help" or "netdrive image help"
to get detailed help for those commands.
*/
package main

import (
	"brutman.com/brutman/netdrive/create"
	"brutman.com/brutman/netdrive/globals"
	"brutman.com/brutman/netdrive/image"
	"brutman.com/brutman/netdrive/mlog"
	"brutman.com/brutman/netdrive/serve"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"time"
)

func usage() {
	fmt.Println(
		`
Usage: netdrive [common flags] command [command flags] <command parms ...>

Commands are:

  create         Create a floppy or hard disk image
  image          Perform image maintenance
  serve          Run the disk server

Common flags for all commands:

  -log_file  <filename>     Set a filename to log to, default is no logging
  -log_level <level>        Log level (debug|info|warn|error), default is info

Use "netdrive create help", "netdrive serve help" or "netdrive image help"
to get help for those commands.
`)
}

var startMsg = "mTCP NetDrive by M Brutman (mbbrutman@gmail.com) (C)opyright 2023-2026, Version: " + globals.BuildDate + "\n"

func main() {

	rand.Seed(time.Now().UnixNano())
	fmt.Println(startMsg)

	if len(os.Args) < 2 {
		fmt.Println("Specify a subcommand for netdrive")
		usage()
		return
	}

	// Common flags for all commands - just logging stuff for now.

	logLevel := flag.String("log_level", "info", "Default logging level. (DEBUG|INFO|WARN|ERROR)")
	logFile := flag.String("log_file", "", "File to write log to")

	flag.Usage = usage
	flag.Parse()

	if err := mlog.SetLogLevel(*logLevel); err != nil {
		fmt.Println(err)
		usage()
		return
	}

	if *logFile != "" {
		if err := mlog.SetLogOutput(*logFile); err != nil {
			fmt.Println("Unable to open log file:", err)
			return
		}

		// If we are logging to a file write a special log message at the start
		// to show the startup message and build date and full parameters.
		mlog.Always(startMsg)
		mlog.Always("Args: %v\n", os.Args)
	}

	// Usage errors print an error string and return a nil for error.  Only
	// runtime errors afer parsing are logged as errors.

	switch strings.ToLower(flag.Args()[0]) {
	case "create":
		err := create.Create(flag.Args()[1:])
		if err != nil {
			mlog.Error("Create error: %s\n", err)
		}
	case "serve":
		err := serve.Serve(flag.Args()[1:])
		if err != nil {
			mlog.Error("Serve error: %s\n", err)
		}
	case "image":
		err := image.CmdLine(flag.Args()[1:])
		if err != nil {
			mlog.Error("Image error: %s\n", err)
		}
	case "help":
		usage()
	default:
		fmt.Println("Unknown command:", flag.Args()[0])
		usage()
	}

	mlog.Shutdown()

}
