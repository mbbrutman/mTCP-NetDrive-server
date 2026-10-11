/*

   mTCP NetDrive Server (crc.go)
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


   Description: my little 16 bit CRC-CCITT algorithm used on data when
   a write-verify command is used.  Adapted from
   https://barrgroup.com/blog/crc-series-part-3-crc-implementation-code-cc.

*/

package serve


var crcTable [256]uint16

func CrcInit( ) {

	for dividend := 0; dividend < 256; dividend++ {

		remainder := uint16(dividend << 8)

		for bit := 0; bit < 8; bit++ {
			if remainder & 0x8000 == 0x8000 {
				remainder = (remainder << 1) ^ 0x1021
			} else {
				remainder = (remainder << 1)
			}
		}

		crcTable[dividend] = remainder
	}

}

func ComputeCRC( buffer []byte ) uint16 {

	r := uint16(0)

	for _, v := range buffer {
		r = crcTable[ v ^ byte(r>>8) ] ^ (r<<8)
	}

	return r
}

