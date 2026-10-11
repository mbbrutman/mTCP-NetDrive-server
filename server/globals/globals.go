/*

   mTCP NetDrive Server (globals.go)
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

*/

// Package globals has project-wide types and variables.
package globals

// BuildDate is a text string passed in by the build system that serves as the version number.
//
// To set the buildDate use -ldflags="-X 'brutman.com/brutman/netdrive/globals.BuildDate=${builddate}'"
// where $builddate has the text string.  Please use the YYYY-MM-DD ISO format.
var BuildDate string



// Lba defines our standard Logical Block Address, which is always 32 bits.
type Lba uint32


// FatalError is a type that is used to indicate that an active session
// needs to be terminated immediately due to an unrecoverable error.  (Most
// errors are not fatal and can be reported to the client.)
type FatalError struct {
	E error
}

// Error returns the underlying error in a FatalError
func (f *FatalError) Error() string {
	return f.E.Error()
}
