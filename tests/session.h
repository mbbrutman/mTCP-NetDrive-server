
/* mTCP NetDrive protocol test (session.h)

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


   Description: A class that defines abstracts NetDrive client session.
*/


#ifndef _SESSION_H
#define _SESSION_H


#include <stdint.h>
#include <stdlib.h>
#include "command.h"


class Session {
  public:
    Session(uint16_t v);
    uint8_t lastResult(void) { return response.getResult( ); }
    void connect( const char filename[] );
    void disconnect( void );
    void read( uint32_t start, uint16_t count, uint8_t buffer[], int bufLen );
    void write( uint32_t start, uint16_t count, uint8_t buffer[], int bufLen );
    void writeZero( uint32_t start, uint16_t count );
    void markCheckpoint( const char tag[] );
    void gotoCheckpoint( const char tag[], bool delMarker );
    void gotoCheckpoint( int index, bool delMarker );
    void listCheckpoints( uint16_t startLine, uint16_t bufferLen );

    const char *testName;

    uint16_t version;
    uint16_t session;
    uint16_t sequence;

    Command request, response;

};


#endif
