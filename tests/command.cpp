
/* mTCP NetDrive protocol test (commmand.cpp)

   Copyright (C) 2024-2026 Michael B. Brutman (mbbrutman@gmail.com)

   This file is part of mTCP NetDrive.

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


   Description: A class that defines abstracts a NetDrive command header.

*/


#include <string.h>

#include "command.h"

int Command::PayloadMaxLen[] = {0, 1200, 1200, 2048};

Command::Command( uint16_t version_p, uint16_t session_p, uint16_t sequence_p,
                  uint8_t op_p, uint8_t result_p, uint32_t start_p, uint16_t count_p) {

  // Fields that are common to all versions map to the same v1 location.
  c.v1.hdr.Version = version_p;
  c.v1.hdr.Session = session_p;
  c.v1.hdr.Sequence = sequence_p;
  c.v1.hdr.Operation = op_p;
  c.v1.hdr.Result = result_p;
  c.v1.hdr.StartSec = start_p;
  c.v1.hdr.SecCount = count_p;

  payloadLen = 0;
}

int Command::getHeaderLen( void ) {
  switch ( c.v1.hdr.Version ) {
    case 1:
    case 2:
      return sizeof(Command_header_v2);
    case 3:
      return sizeof(Command_header_v3);
  }

  // Default used on an invalid version
  return 14;
}


// Copy a user buffer into the payload area.  Protected against
// overflowing the payload area.

void Command::setPayload( const uint8_t *payload_p, int payloadLen_p ) {
  int copyLen = payloadLen_p;
  if ( copyLen > PayloadMaxLen[ getVersion( ) ] ) {
    copyLen = PayloadMaxLen[ getVersion( ) ];
  }
  memcpy( getPayloadStart(), payload_p, copyLen );
  payloadLen = copyLen;
}

