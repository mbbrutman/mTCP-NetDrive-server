/*

   mTCP NetDrive Server (fletcher.go)
   Copyright (C) 2026-2026 Michael B. Brutman (mbbrutman@gmail.com)
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


   Description: The Fletcher-32 checksum, as described by Wikipedia.
    https://en.wikipedia.org/wiki/Fletcher%27s_checksum

*/


package serve

import "encoding/binary"


func ComputeFletcher32( buffer []byte ) uint32 {

	c0 := uint32(0)
	c1 := uint32(0)

	l := len(buffer) / 2
	i := 0

	for l > 0 {

		blockLen := l
		if blockLen > 360 {
			blockLen = 360
		}
		l = l - blockLen

		for ; blockLen > 0 ; blockLen-- {
			word := binary.LittleEndian.Uint16(buffer[i*2:i*2+2])
			c0 = c0 + uint32(word)
			c1 = c1 + c0
			i++
		}

		c0 = c0 % 65535
		c1 = c1 % 65535

	}

	return ( ((c1 & 0xffff) << 16) | (c0 & 0xffff) )
}
