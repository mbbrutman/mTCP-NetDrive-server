

/* mTCP NetDrive protocol test (session.cpp)

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

#include <string.h>

#include "session.h"




extern int executeCommand( const char *name, Command *request, Command *resp );
extern Command buildConnectCommand( uint16_t version, uint16_t seq, const char filename[] );


Session::Session( uint16_t v ) {
  version = v;
  session = 0;
  sequence = rand( );
  testName = "TestName not set";
}


void Session::connect( const char filename[] ) {

  request = buildConnectCommand( version, sequence, filename );

  executeCommand(testName, &request, &response);
  session = response.getSession( );
  sequence++;
}

void Session::disconnect( void ) {
  request = Command(version, session, sequence, Op_Disconnect, 0, 0, 0);
  executeCommand(testName, &request, &response);
  sequence++;
}

void Session::read( uint32_t start, uint16_t count, uint8_t buf[], int bufLen ) {

  request = Command(version, session, sequence, Op_Read, 0, start, count);
  executeCommand(testName, &request, &response);
  sequence++;

  // If they provided a buffer then copy the response payload to it.
  if ( buf != NULL ) {
    if ( response.getResult( ) == 0 ) {
      int copyLen = count * 512;
      if ( copyLen > bufLen ) copyLen = bufLen;
      memcpy( buf, response.getPayloadStart( ), bufLen );
    }
  }
}

void Session::write( uint32_t start, uint16_t count, uint8_t buffer[], int bufLen ) {
  request = Command(version, session, sequence, Op_Write, 0, start, count);
  request.setPayload( buffer, bufLen );
  executeCommand(testName, &request, &response);
  sequence++;
}


// Variant of the write command that doesn't require the caller to provide
// data.  It also allows for large sector count numbers safely, as it won't
// zero out the payload past its end.  This is used for testing an invalid
// number of sectors.
void Session::writeZero( uint32_t start, uint16_t count ) {

  request = Command(version, session, sequence, Op_Write, 0, start, count);

  // We might want to pass in a garbage count parameter which can cause a
  // buffer overflow.  Allow it, but don't do the bzero.
  uint8_t tmpPayload[2048];
  int l = count*512;
  if ( l > 2048 ) {l = 2048;}
  bzero(tmpPayload, l);
  request.setPayload( tmpPayload, l );

  executeCommand(testName, &request, &response);
  sequence++;
}

void Session::markCheckpoint( const char tag[] ) {
  request = Command(version, session, sequence, Op_Mark_Chkp, 0, 0, 0);
  request.setPayload( (uint8_t *)tag, strlen(tag) );
  executeCommand(testName, &request, &response);
  sequence++;
}

void Session::gotoCheckpoint( const char tag[], bool delMarker ) {

  request = Command(version, session, sequence, Op_Goto_Chkp, 0, 0, 0);

  uint8_t tmpPayload[1024];
  tmpPayload[0] = 0;  // Type: tag
  tmpPayload[1] = (delMarker ? 1 : 0);  // Delete the marker?
  memcpy(tmpPayload+4, tag, strlen(tag));

  request.setPayload( tmpPayload, 4 + strlen(tag) );

  executeCommand(testName, &request, &response);
  sequence++;
}

void Session::gotoCheckpoint( int index, bool delMarker ) {

  request = Command(version, session, sequence, Op_Goto_Chkp, 0, 0, 0);

  uint8_t tmpPayload[4];
  tmpPayload[0] = 1;                    // Type: numeric index
  tmpPayload[1] = (delMarker ? 1 : 0);  // Delete the marker?
  tmpPayload[2] = index;                // Least significant byte
  tmpPayload[3] = index >> 8;           // Most significant byte

  request.setPayload( tmpPayload, 4 );

  executeCommand(testName, &request, &response);
  sequence++;
}

// Even if the server reports the command was successful, if we can't
// parse it that is an error and this modifies the returned command
// to indicate that.

extern int parseCheckpointList( char *payload, int payloadLen );

void Session::listCheckpoints( uint16_t startLine, uint16_t bufferLen ) {

  switch ( version ) {

    case 2: {
      // startLine and bufferLen are not used on v2.
      request = Command(version, session, sequence, Op_List_Chkp, 0, 0, 0);
      executeCommand(testName, &request, &response);
      if (parseCheckpointList( (char *)response.getPayloadStart( ), response.getPayloadLen( )) != 0 ) {
        response.setResult( 1 );
      }
      sequence++;
      break;
    }
    case 3: {
      request = Command(version, session, sequence, Op_List_Chkp, 0, 0, 0);
      Payload_List_Chkp_v3_req_t rp;
      rp.startLine = startLine;
      rp.bufferSize = bufferLen;
      request.setPayload( (uint8_t *)(&rp), 4 );
      executeCommand(testName, &request, &response);
      Payload_List_Chkp_v3_resp_t *responsePayload = (Payload_List_Chkp_v3_resp_t *)response.getPayloadStart( );
      int tmpLen = response.getPayloadLen( ) - sizeof( Payload_List_Chkp_v3_resp_t );
      if (parseCheckpointList( ((char *)responsePayload) + sizeof( Payload_List_Chkp_v3_resp_t ), response.getPayloadLen( )) != 0 ) {
        response.setResult( 1 );
      }
      sequence++;
      break;
    }

  }

}



