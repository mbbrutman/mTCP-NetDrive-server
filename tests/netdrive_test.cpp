/* mTCP NetDrive protocol test

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


   Description: Some basic testing for the mTCP NetDrive server

*/



#include <errno.h>
#include <stdio.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <sys/select.h>
#include <time.h>
#include <unistd.h>

#include <sys/socket.h>
#include <netinet/in.h>
#include <netinet/udp.h>

#include <netdb.h>

#include "command.h"
#include "session.h"





// Globals for socket management

int Socket_fd;
struct addrinfo *Server;





// buildConnectCommand
// 
// Creates a well-formed connect command for the different versions.

Command buildConnectCommand( uint16_t version, uint16_t seq, const char filename[] ) {

  Command tmp = Command( version, 0, seq, Op_Connect, 0, 0, 0 );

  switch ( version ) {
    case 1: {
      Payload_Connect_v1_t *p = (Payload_Connect_v1_t *)tmp.getPayloadStart( );
      for (int i=0; i < 6; i++) p->macAddr[i] = rand();
      strcpy( p->filename, filename );
      tmp.setPayloadLen( 6 + strlen(filename) + 1 );
      break;
    }
    case 2:
    case 3: {
      Payload_Connect_v2_t *p = (Payload_Connect_v2_t *)tmp.getPayloadStart( );
      for (int i=0; i < 6; i++) p->macAddr[i] = rand();
      p->clientType = 1;
      p->blockSize = 512;
      strcpy( p->osString, "DOS 6.8" );
      strcpy( p->filename, filename );
      tmp.setPayloadLen( 6 + 2 + 2 + 40 + strlen(filename) + 1 );
      break;
    }
  }

  return tmp;
}





char checkpointList[100][100];
int checkpointListLen;

int parseCheckpointList( char *payload, int payloadLen ) {

  // Payload is not null terminated, so fix that here.
  payload[payloadLen] = 0;

  checkpointListLen = 0;
  int i=0;
  int j=0;

  while ( i < payloadLen ) {

    if ( payload[i] == '\n' ) {
      checkpointList[checkpointListLen][j] = 0;
      checkpointListLen++;
      j = 0;
    } else {
      // Protect us from long lines which are not illegal, but inconvenient.
      if ( j < 99 ) checkpointList[checkpointListLen][j++] = payload[i];
    }

    i++;
  }

  return 0;
}



// Utils::dumpBytes
//
// Just a generic utility to do a nice hexidecimal dump of data.
//
// The original code was simple but made 17 calls to fprintf
// for each line of output.  This version only calls fputs
// once for each line of output.  The code size is about the
// same but this requires some extra static storage for the
// output buffer.

// Output format (for figuring out column locations):
//
// 0                                                 50               67
// v                                                 v                v
// xx xx xx xx xx xx xx xx xx xx xx xx xx xx xx xx   0123456789abcdef_

static char dumpBytesBuffer[68];
static const char hexDigits[] = "0123456789ABCDEF";

void dumpBytes( FILE *stream, unsigned char *buffer, unsigned int len ) {

  dumpBytesBuffer[0] = 0;  // Set to null in case they give us len=0;
  dumpBytesBuffer[48] = ' ';
  dumpBytesBuffer[49] = ' ';

  int index1 = 0;
  int index2 = 50;

  int i;
  for ( i = 0; i < len; i++ ) {

    dumpBytesBuffer[index2++] = ((buffer[i] > 31) && (buffer[i]<127)) ? buffer[i] : '.';

    dumpBytesBuffer[index1++] = hexDigits[(buffer[i] >> 4)];
    dumpBytesBuffer[index1++] = hexDigits[(buffer[i] & 0xF)];
    dumpBytesBuffer[index1++] = ' ';

    if ( index2 == 66 ) {
      dumpBytesBuffer[ index2++ ] = '\n';
      dumpBytesBuffer[ index2 ] = 0;
      fputs( dumpBytesBuffer, stream );
      index1 = 0;
      index2 = 50;
    }

  }

  if ( index1 ) {
    while ( index1 < 48 ) dumpBytesBuffer[index1++] = ' ';
    dumpBytesBuffer[ index2++ ] = '\n';
    dumpBytesBuffer[ index2++ ] = '\n';  // This was a partial line so we know there is room.
    dumpBytesBuffer[ index2 ] = 0;
  }
  else {
    dumpBytesBuffer[ 0 ] = '\n';
    dumpBytesBuffer[ 1 ] = 0;
  }

  fputs( dumpBytesBuffer, stream );
}

int bufferCompare( uint8_t b1[], uint8_t b2[], int len ) {
  for ( int i=0; i < len; i++ ) {
    if ( b1[i] != b2[i] ) {
      printf("\nData compare error, first bad byte: %u\n", i);
      dumpBytes( stdout, b1, len );
      dumpBytes( stdout, b2, len );
      return 1;
    }
  }
  return 0;
}



const char testFmtString[] = "  Test: %-75s %s\n";

bool testResultMsg( const char *msg, bool b ) {
  const char *result;
  if (b) {
    result = "\x1b[32mPassed\x1b[0m";
  } else {
    result = "\x1b[31mFailed\x1b[0m";
  }
  printf( testFmtString, msg, result );
  return b;
}




// executeCommand
//
// Send a command to the server and wait for a result.  The result is
// placed in resp.  Generally this is used to run a command see if there
// is a good or a bad return code, but the entire reponse is available
// to examine.
//
// Returns
//
//   0: got some sort of a response.
//   1: time-out waiting for a response.
//   exit the program on a hard error

int executeCommand( const char *name, Command *request, Command *resp ) {

  if (sendto(Socket_fd, request->getDataStart(), request->getTotalLen( ), 0, Server->ai_addr, Server->ai_addrlen) < 0) {
    fprintf(stderr, "\nError running test \"%s\": Error sending packet: %s\n", name, strerror(errno));
    exit(1);
  }

  fd_set rfds;
  FD_ZERO(&rfds);
  FD_SET(Socket_fd, &rfds);

  struct timeval tv;
  tv.tv_sec = 3;
  tv.tv_usec = 0;

  int rc = select(Socket_fd+1, &rfds, NULL, NULL, &tv);
  if ( rc == -1 ) {
    fprintf(stderr, "\nError running test \"%s\": Error on select call: %s\n", name, strerror(errno));
    exit(1);
  } else if ( rc == 0 ) {
    // Timeout
    return 1;
  }

  int bytesReceived = recvfrom(Socket_fd, resp->getDataStart(), 1500, 0, NULL, NULL);
  if ( bytesReceived < 0 ) {
    fprintf(stderr, "\nError running test \"%s\": Error on recvfrom call: %s\n", name, strerror(errno));
    exit(1);
  }

  if ( bytesReceived < sizeof(Command_header_v1) ) {
    fprintf(stderr, "\nError running test \"%s\": Not enough data received.\n", name);
    exit(1);
  }

  // We thing resp is a command header and a response, but we really don't know.
  // Do a basic version number check before trying to set the payload length.

  if ( resp->getVersion( ) > 0 || resp->getVersion( ) < 4 ) {
    resp->setPayloadLen( bytesReceived - resp->getHeaderLen( ) );
  } else {
    fprintf(stderr, "\nError running test \"%s\": Illegal version number in response.\n", name);
    exit(1);
  }

  return 0;
}


// executeTest
//
// This is little more involved.  It sends a command, and then compares
// the result to a prepopulated result buffer.
//
// * If the response is shorter than the expected response length the
//   test is marked as failed.
//
// * If the reponse has a good return code then the length and the
//   bytes have to match exactly.
//
// * If the reponse has a bad return code then the received bytes may be
//   longer than the expected bytes; this is because the server might add
//   an optional error message.  Only compare the number of expected bytes,
//   which should be the header.
//
//   This can also hang up, as there is no timeout or retries yet.

bool executeTest( const char *name, Command request, Command response ) {

  bool success = true;

  uint8_t buffer[1500];

  if (sendto(Socket_fd, request.getDataStart(), request.getTotalLen(), 0, Server->ai_addr, Server->ai_addrlen) < 0) {
    fprintf(stderr, "\nError running test \"%s\": Error sending packet: %s\n", name, strerror(errno));
    exit(1);
  }

  fd_set rfds;
  FD_ZERO(&rfds);
  FD_SET(Socket_fd, &rfds);

  struct timeval tv;
  tv.tv_sec = 3;
  tv.tv_usec = 0;

  int rc = select(Socket_fd+1, &rfds, NULL, NULL, &tv);
  if ( rc == -1 ) {
    fprintf(stderr, "\nError running test \"%s\": Error on select call: %s\n", name, strerror(errno));
    exit(1);
  } else if ( rc == 0 ) {
    // Timeout
    fprintf(stderr, "\nError running test \"%s\": Timeout\n", name);
    exit(1);
  }

  int bytesReceived = recvfrom(Socket_fd, buffer, 1500, 0, NULL, NULL);
  if ( bytesReceived < 0 ) {
    fprintf(stderr, "\nError running test \"%s\": Error on recvfrom call: %s\n", name, strerror(errno));
    exit(1);
  }

  if ( bytesReceived < response.getTotalLen() ) {
    success == false;
  } else {

    Command tmp;
    memcpy( tmp.getDataStart( ), buffer, bytesReceived );
    tmp.setPayloadLen( bytesReceived - tmp.getHeaderLen( ) );

    int compareLen = bytesReceived;

    if ( tmp.getResult( ) == 0 ) {
      if ( bytesReceived != response.getTotalLen( ) ) success == false;
    }
    else {
      if ( compareLen > response.getTotalLen() ) compareLen = response.getTotalLen();
    }

    if ( success == true ) {
      for ( int i=0; i < compareLen; i++ ) {
        if ( buffer[i] != ((uint8_t *)response.getDataStart())[i] ) {
          success == false;
        }
      }
    }

  }

  if ( !testResultMsg( name, success ) ) {

    puts("\nCommand:");
    dumpBytes( stdout, (uint8_t *)(request.getDataStart()), request.getTotalLen() );
    puts("Expected:");
    dumpBytes( stdout, (uint8_t *)(response.getDataStart()), response.getTotalLen() );
    puts("Received:");
    dumpBytes( stdout, buffer, bytesReceived );
  }

  return success;
}



int protocolTests( void ) {

  const char *testName = NULL;

  puts("Protocol tests (basic)");
  uint16_t sequence = rand();

  // Send a bad protocol version, get an error return code and
  // the protocol version of the server.
  Command c = Command(0, 0, sequence, Op_Connect, 0, 0, 0);
  Command r = Command(3, 0, sequence++, Op_Connect, 1, 0, 0);
  if ( !executeTest("Version 0 is not valid, should be rejected", c, r) ) return 1;

  // Same as above, but with a protocol version newer than the server.
  c = Command(4, 0, sequence, Op_Connect, 0, 0, 0);
  r = Command(3, 0, sequence++, Op_Connect, 1, 0, 0);
  if ( !executeTest("Version number above server version, should be rejected", c, r) ) return 1;

  // Try to use session number 0, which is reserved for connect.
  c = Command(1, 0, sequence, Op_Read, 0, 0, 0);
  r = Command(1, 0, sequence++, Op_Read, 1, 0, 0);
  if ( !executeTest("Session number is 0 and cmd is not connect, should be rejected", c, r) ) return 1;



  // Connect, change the protocol version and see if the server complains
  // the server should just ignore it outright, meaning we will timeout.
  sequence = rand();
  testName = "V1: Connect (start a session)";
  c = buildConnectCommand(1, sequence++, "rw_journaled.dsk");
  executeCommand(testName, &c, &r);
  if ( !testResultMsg( testName, r.getResult( ) == 0 ) ) return 1;

  uint16_t session = r.getSession( );

  testName = "V1: Test read of first block";
  c = Command( 1, session, sequence++, Op_Read, 0, 0, 1 );
  executeCommand(testName, &c, &r);
  if ( !testResultMsg(testName, r.getResult( ) == 0) ) return 1;


  // Don't enforce this strictly yet; v2 clients have a bug that violates this
  // and we don't want to break them yet.
  testName = "V2: Test read of first block w/ bad version, should be ignored";
  c = Command( 2, session, sequence++, Op_Read, 0, 0, 1 );
  int timeout = executeCommand(testName, &c, &r);
  if ( testResultMsg(testName, timeout == 1) ) {
    // If you are here you timed out correctly.  Put the sequence number
    // back to where it should be.
    sequence--;
  }


  testName = "V1: Disconnect";
  c = Command( 1, session, sequence++, Op_Disconnect, 0, 0, 0);
  executeCommand(testName, &c, &r);
  if ( !testResultMsg( testName, r.getResult( ) == 0 ) ) return 1;



  // Connect, send a valid command, get a response, and then send another
  // different but valid command without bumping the sequence number.  (The
  // command is different so it doesn't look like a retry.)  We should get
  // an error back saying the command failed even though it would be valid
  // with a good sequence number.

  sequence = rand();

  testName = "V1: Connect (start a session)";
  c = buildConnectCommand(1, sequence++, "rw_journaled.dsk");
  executeCommand(testName, &c, &r);
  if ( !testResultMsg( testName, r.getResult( ) == 0 ) ) return 1;

  session = r.getSession( );

  testName = "V1: Test read of first block";
  c = Command( 1, session, sequence, Op_Read, 0, 0, 1 );
  executeCommand(testName, &c, &r);
  if ( !testResultMsg(testName, r.getResult( ) == 0) ) return 1;

  testName = "V1: New read w/ same sequence number, should be rejected";
  c = Command( 1, session, sequence++, Op_Read, 0, 0, 2 );
  executeCommand(testName, &c, &r);
  if ( !testResultMsg(testName, r.getResult( ) == 1) ) return 1;

  testName = "V1: Disconnect";
  c = Command( 1, session, sequence++, Op_Disconnect, 0, 0, 0);
  executeCommand(testName, &c, &r);
  if ( !testResultMsg(testName, r.getResult( ) == 0 ) ) return 1;

  return 0;
}


int recognizedCommandsTest( void ) {

  const char *testName = NULL;

  puts("Protocol version/recognized commands tests");

  uint16_t sequence = rand();
  Command r;

  
  // Version 1 tests
  //
  // Use a journaled image to ensure that we get errors back because the
  // commands are invalid, not because the image type is wrong.

  testName = "V1: Connect (start a session)";
  Command c = buildConnectCommand(1, sequence++, "rw_journaled.dsk");
  executeCommand(testName, &c, &r);
  if ( !testResultMsg( testName, r.getResult( ) == 0 ) ) return 1;

  uint16_t session = r.getSession( );

  // Command 0 does not exist - reject it as an error.
  testName = "V1: Command 0 is invalid, should be rejected";
  c = Command(1, session, sequence++, Op_not_valid, 0, 0, 0);
  executeCommand(testName, &c, &r);
  if ( !testResultMsg( testName, r.getResult( ) == 1 ) ) return 1;

  // Version 1 doesn't know about Op_Mark_Chkp
  testName = "V1: Op_Mark_Chkp is invalid for V1, should be rejected";
  c = Command(1, session, sequence++, Op_Mark_Chkp, 0, 0, 0);
  executeCommand(testName, &c, &r);
  if ( !testResultMsg( testName, r.getResult( ) == 1 ) ) return 1;

  // Bonus points ... retry it.
  testName = "V1: Op_Mark_Chkp is invalid for V1, retry, should be rejected";
  c = Command(1, session, sequence++, Op_Mark_Chkp, 0, 0, 0);
  executeCommand(testName, &c, &r);
  if ( !testResultMsg( testName, r.getResult( ) == 1 ) ) return 1;

  testName = "V1: Disconnect";
  c = Command(1, session, sequence++, Op_Disconnect, 0, 0, 0);
  executeCommand(testName, &c, &r);
  if ( !testResultMsg( testName, r.getResult( ) == 0 ) ) return 1;


  // Version 2 tests

  sequence = rand();

  testName = "V2: Connect (start a session)";
  c = buildConnectCommand(2, sequence++, "rw.dsk");
  executeCommand(testName, &c, &r);
  if ( !testResultMsg( testName, r.getResult( ) == 0 ) ) return 1;

  session = r.getSession( );

  testName = "V2: Command 9 is invalid, should be rejected";
  c = Command(2, session, sequence++, Operation_t(Op_List_Chkp+1), 0, 0, 0);
  executeCommand(testName, &c, &r);
  if ( !testResultMsg( testName, r.getResult( ) == 1 ) ) return 1;

  testName = "V2: Disconnect";
  c = Command(2, session, sequence++, Op_Disconnect, 0, 0, 0);
  executeCommand(testName, &c, &r);
  if ( !testResultMsg( testName, r.getResult( ) == 0 ) ) return 1;


  // Version 3 tests

  sequence = rand();

  testName = "V3: Connect";
  c = buildConnectCommand(3, sequence++, "rw.dsk");
  executeCommand(testName, &c, &r);
  if ( !testResultMsg( testName, r.getResult( ) == 0 ) ) return 1;

  session = r.getSession( );

  testName = "V3: Command 9 is invalid, should be rejected";
  c = Command(3, session, sequence++, Operation_t(Op_List_Chkp+1), 0, 0, 0);
  executeCommand(testName, &c, &r);
  if ( !testResultMsg( testName, r.getResult( ) == 1 ) ) return 1;

  testName = "V3: Disconnect";
  c = Command(3, session, sequence++, Op_Disconnect, 0, 0, 0);
  executeCommand(testName, &c, &r);
  if ( !testResultMsg( testName, r.getResult( ) == 0 ) ) return 1;

  return 0;
}


int connectTests( void ) {

  puts("Connect tests");
  uint16_t sequence = rand();

  Command c = Command(1, 1, sequence, Op_Connect, 0, 0, 0);
  Command r = Command(1, 1, sequence++, Op_Connect, 1, 0, 0);
  if ( !executeTest("V1: Connect with session != 0, should be rejected", c, r) ) return 1;

  c = Command(1, 0, sequence, Op_Connect, 0, 0, 0);
  r = Command(1, 0, sequence++, Op_Connect, 1, 0, 0);
  if ( !executeTest("V1: Connect with no payload, should be rejected", c, r) ) return 1;

  c = Command(1, 0, sequence, Op_Connect, 0, 0, 0);
  c.setPayload( (const uint8_t[]){0xff, 0xfe, 0xfd, 0xfc, 0xfb, 0xfa}, 6 );
  r = Command(1, 0, sequence++, Op_Connect, 1, 0, 0);
  if ( !executeTest("V1: Connect with no image name, should be rejected", c, r) ) return 1;

  // This test also covers an image name that is too long, as there
  // Will be no terminating nul char.
  c = Command(1, 0, sequence, Op_Connect, 0, 0, 0);
  c.setPayload( (const uint8_t[]){0xff, 0xfe, 0xfd, 0xfc, 0xfb, 0xfa, 'b', 'o', 'g', 'u', 's', 0x00}, 11);
  r = Command(1, 0, sequence++, Op_Connect, 1, 0, 0);
  if ( !executeTest("V1: Connect with bad image name (no nul), should be rejected", c, r) ) return 1;

  c = buildConnectCommand(1, sequence, "bogus");
  r = Command(1, 0, sequence++, Op_Connect, 1, 0, 0);
  if ( !executeTest("V1: Connect with non-existent image name, should be rejected", c, r) ) return 1;

  c = buildConnectCommand(1, sequence, ".ogus");
  r = Command(1, 0, sequence++, Op_Connect, 1, 0, 0);
  if ( !executeTest("V1: Connect with image name (illegal char) 1, should be rejected", c, r) ) return 1;

  c = buildConnectCommand(1, sequence, "..gus");
  r = Command(1, 0, sequence++, Op_Connect, 1, 0, 0);
  if ( !executeTest("V1: Connect with image name (illegal char) 2, should be rejected", c, r) ) return 1;

  c = buildConnectCommand(1, sequence, "-ogus");
  r = Command(1, 0, sequence++, Op_Connect, 1, 0, 0);
  if ( !executeTest("V1: Connect with image name (illegal char) 3, should be rejected", c, r) ) return 1;


  // V2 specific tests

  c = buildConnectCommand(2, sequence, "");
  r = Command(2, 0, sequence++, Op_Connect, 1, 0, 0);
  if ( !executeTest("V2: Connect with no image name, should be rejected", c, r) ) return 1;

  c = buildConnectCommand(2, sequence, "bogus");
  r = Command(2, 0, sequence++, Op_Connect, 1, 0, 0);
  memset(c.getPayloadStart()+10, 'A', 40);
  if ( !executeTest("V2: Connect OS string not null terminated, should be rejected", c, r) ) return 1;

  c = buildConnectCommand(2, sequence, "bogus");
  r = Command(2, 0, sequence++, Op_Connect, 1, 0, 0);
  *(c.getPayloadStart()+55) = '*';
  if ( !executeTest("V2: Connect image name not null terminated, should be rejected", c, r) ) return 1;

  return 0;
}




int imageLimitsTest( void ) {

  const char *testName = NULL;

  puts("Image limits tests");

  Session s = Session(1);
  
  s.testName = "V1: Connect to rw.dsk";
  s.connect("rw.dsk");
  if ( !testResultMsg( s.testName, s.lastResult()==0 ) ) return 1;

  s.testName = "V1: Read last block";
  s.read(2047, 1, NULL, 0);
  if ( !testResultMsg( s.testName, s.lastResult()==0 ) ) return 1;

  s.testName = "V1: Read past last block (1 block), should be rejected";
  s.read(2048, 1, NULL, 0);
  if ( !testResultMsg( s.testName, s.lastResult()==1 ) ) return 1;

  s.testName = "V1: Read past last block (2 blocks), should be rejected";
  s.read(2047, 2, NULL, 0);
  if ( !testResultMsg( s.testName, s.lastResult()==1 ) ) return 1;

  s.testName = "V1: Read 0 blocks (protocol error), should be rejected";
  s.read(0, 0, NULL, 0);
  if ( !testResultMsg( s.testName, s.lastResult()==1 ) ) return 1;

  s.testName = "V1: Read 3 blocks (protocol error), should be rejected";
  s.read(0, 3, NULL, 0);
  if ( !testResultMsg( s.testName, s.lastResult()==1 ) ) return 1;

  s.testName = "V1: Write past last block (1 block), should be rejected";
  s.writeZero(2048, 1);
  if ( !testResultMsg( s.testName, s.lastResult()==1 ) ) return 1;

  s.testName = "V1: Write past last block (2 blocks), should be rejected";
  s.writeZero(2047, 2);
  if ( !testResultMsg( s.testName, s.lastResult()==1 ) ) return 1;

  s.testName = "V1: Write 0 blocks (protocol error), should be rejected";
  s.writeZero(0, 0);
  if ( !testResultMsg( s.testName, s.lastResult()==1 ) ) return 1;

  // writeZero is the only variant of this command where it is safe to
  // pass in a larger sector number than 2.
  s.testName = "V1: Write 3 blocks (protocol error), should be rejected";
  s.writeZero(0, 3);
  if ( !testResultMsg( s.testName, s.lastResult()==1 ) ) return 1;

  // Craft a write command with a bad payload length
  testName = "V1: Write 1 block w/ short payload, should be rejected";
  Command c = Command(1, s.session, s.sequence, Op_Write, 0, 1, 1);
  Command r;
  c.setPayloadLen( 511 );
  executeCommand(testName, &c, &r);
  s.sequence++;
  if ( !testResultMsg( testName, r.getResult( ) == 1 ) ) return 1;

  s.testName = "V1: Disconnect";
  s.disconnect();
  if ( !testResultMsg( s.testName, s.lastResult()==0 ) ) return 1;



  s = Session(3);

  s.connect("rw.dsk");
  if ( !testResultMsg( "V3: Connect to rw.dsk", s.lastResult()==0 ) ) return 1;

  // writeZero is the only variant of this command where it is safe to
  // pass in a larger sector number than 2.
  //
  // Write near the end of the image to avoid blasting things that we need.
  s.writeZero(2000, 3);
  if ( !testResultMsg( "V3: Write 3 blocks, should be allowed", s.lastResult() == 0 ) ) return 1;

  s.disconnect();
  if ( !testResultMsg( "V3: Disconnect", s.lastResult()==0 ) ) return 1;

  return 0;
}


int checkpointTests_v2( void ) {

  const char *testName = NULL;

  puts("Checkpoints V2: Simple Mark, Write, Goto");

  Session s = Session(2);

  s.testName = "V2: Connect to journaled image";
  s.connect("rw_journaled.dsk");
  if ( !testResultMsg( s.testName, s.lastResult() == 0 ) ) return 1;

  uint8_t origData[512];
  s.testName = "V2: Read sector 0";
  s.read(0, 1, origData, 512);
  if ( !testResultMsg( s.testName, s.lastResult() == 0 ) ) return 1;

  // Give checkpoint list scan some time to run
  sleep(1);


  // List checkpoints: should always start at 1.
  s.testName = "V2: List checkpoints: list received";
  s.listCheckpoints( 0, 0 );
  if ( !testResultMsg( s.testName, s.lastResult() == 0) ) return 1;


  s.testName = "V2: List checkpoints: oldest index is 1";
  int oldestCheckpointMarkIndex = atoi(checkpointList[checkpointListLen-1]);
  if (oldestCheckpointMarkIndex == 1) {
    testResultMsg( s.testName, true );
  } else {

    printf( testFmtString, testName, "\x1b[33mFailed, Retrying\x1b[0m" );

    s.testName = "V2: Goto checkpoint index 4";
    s.gotoCheckpoint( 4, false );
    if ( !testResultMsg( s.testName, s.lastResult()==0) ) return 1;

    s.testName = "V2: List checkpoints: list received";
    s.listCheckpoints( 0, 0 );
    if ( !testResultMsg( s.testName, s.lastResult() == 0) ) return 1;

    s.testName = "V2: List checkpoints: oldest index is 1";
    oldestCheckpointMarkIndex = atoi(checkpointList[checkpointListLen-1]);
    if ( !testResultMsg( s.testName, oldestCheckpointMarkIndex==1) ) return 1;

  }

  // Goto with a bad index number
  s.testName = "V2: Goto checkpoint: index 0, should be rejected";
  s.gotoCheckpoint( 0, false );
  if ( !testResultMsg( s.testName, s.lastResult() == 1) ) return 1;

  // Goto with a marker we know doesn't exist
  s.testName = "V2: Goto checkpoint: unknown tag, should be rejected";
  s.gotoCheckpoint( "bogus", false );
  if ( !testResultMsg( s.testName, s.lastResult() == 1) ) return 1;

  // Goto with a 0 length tag
  s.testName = "V2: Goto checkpoint: 0 length tag, should be rejected";
  s.gotoCheckpoint( "", false );
  if ( !testResultMsg( s.testName, s.lastResult() == 1) ) return 1;

  // Try bad tag: 0 length
  s.testName = "V2: Mark checkpoint: zero length tag, should be rejected";
  s.markCheckpoint("");
  if ( !testResultMsg( s.testName, s.lastResult() == 1) ) return 1;

  // Try invalid tag - too long
  s.testName = "V2: Mark checkpoint: tag too long, should be rejected";
  s.markCheckpoint("01234567890123456789012345678901234567890123456789012345678901234");
  if ( !testResultMsg( s.testName, s.lastResult() == 1) ) return 1;

  // Create a max length tag, then delete it.
  s.testName = "V2: Mark checkpoint: Create maximum length tag (64)";
  s.markCheckpoint("0123456789012345678901234567890123456789012345678901234567890123");
  if ( !testResultMsg( s.testName, s.lastResult() == 0) ) return 1;


  // Create a 3 to 7 letter random tag
  char tag[8];
  int tagLen = (rand() % 5) + 3;
  for (int i=0; i < tagLen; i++ ) { tag[i] = (rand() % 26) + 'A'; }
  tag[tagLen] = 0;


  s.testName = "V2: Mark checkpoint: Create random 3 to 7 letter tag";
  s.markCheckpoint(tag);
  if ( !testResultMsg( s.testName, s.lastResult() == 0 ) ) return 1;

  // Write random garbage, but save what we wrote so we can verify it on readback.
  s.testName = "V2: Write garbage to sector 0";
  uint8_t randomGarbage[512];
  for (int i=0; i < 512; i++) randomGarbage[i] = rand();
  s.write(0, 1, randomGarbage, 512);
  if ( !testResultMsg( s.testName, s.lastResult() == 0 ) ) return 1;


  // Read back random garbage and verify it.
  s.testName = "V2: Read back sector 0";
  uint8_t readback[512];
  s.read(0, 1, readback, 512);
  if ( !testResultMsg( s.testName, s.lastResult()==0 ) ) return 1;
  
  s.testName = "V2: Verify garbage in sector 0";
  if ( !testResultMsg( s.testName, bufferCompare( randomGarbage, readback, 512 ) == 0 ) ) return 1;


  s.testName = "V2: Goto checkpoint: previously randomly generated tag";
  s.gotoCheckpoint(tag, false);
  if ( !testResultMsg( s.testName, s.lastResult() == 0 ) ) return 1;

  s.testName = "V2: Goto checkpoint: Same tag, delete tag marker too";
  s.gotoCheckpoint(tag, true);
  if ( !testResultMsg( s.testName, s.lastResult() == 0 ) ) return 1;


  // Do the same command again, but make it look like a retry.
  // This will get refused because we don't execute retried goto_cp commands.
  s.testName = "V2: Retry Goto checkpoint: Same tag again, should be rejected";
  s.sequence--;
  s.gotoCheckpoint(tag, true);
  if ( !testResultMsg( s.testName, s.lastResult() == 1 ) ) return 1;


  s.testName = "V2: Read sector 0 to confirm orig data";
  s.read(0, 1, readback, 512);
  if ( !testResultMsg( s.testName, s.lastResult()==0 ) ) return 1;

  s.testName = "V2: Verify original good data in sector 0";
  if ( !testResultMsg( s.testName, bufferCompare( origData, readback, 512 ) == 0 ) ) return 1;

  // Undo max length tag
  s.testName = "V2: Goto checkpoint: goto and delete max length tag";
  s.gotoCheckpoint("0123456789012345678901234567890123456789012345678901234567890123", true);
  if ( !testResultMsg( s.testName, s.lastResult() == 0) ) return 1;

  // This tag no longer exists, so this will fail.
  s.testName = "V2: Goto checkpoint: previous random tag, should be rejected";
  s.gotoCheckpoint(tag, true);
  if ( !testResultMsg( s.testName, s.lastResult() == 1 ) ) return 1;


  s.testName = "V2: Read sector 0 to confirm orig data (again)";
  s.read(0, 1, readback, 512);
  if ( !testResultMsg( s.testName, s.lastResult()==0 ) ) return 1;

  s.testName = "V2: Verify original good data in sector 0 (again)";
  if ( !testResultMsg( s.testName, bufferCompare( origData, readback, 512 ) == 0 ) ) return 1;

  s.testName = "V2: Disconnect";
  s.disconnect();
  if ( !testResultMsg( s.testName, s.lastResult() == 0 ) ) return 1;


  return 0;
}



int checkpointTests_v3( void ) {

  puts("Checkpoints V3: List checkpoints with cursor");


  // First test: Validate that the oldest tag returned is tag 1, just line in v2.

  Session s = Session(3);

  s.testName = "V3: Connect to journaled image";
  s.connect("rw_journaled.dsk");
  if ( !testResultMsg( s.testName, s.lastResult()==0 ) ) return 1;

  // Give checkpoint list scan some time to run
  sleep(1);

  // List checkpoints: should always start at 1.
  s.testName = "V3: List checkpoints: receive a list";
  s.listCheckpoints( 0, 400 );
  if ( !testResultMsg( s.testName, s.lastResult()==0) ) return 1;


  s.testName = "V3: List checkpoints: oldest index is 1";
  int oldestCheckpointMarkIndex = atoi(checkpointList[checkpointListLen-1]);

  if (oldestCheckpointMarkIndex == 1) {
    testResultMsg( s.testName, true );
  } else {

    printf( testFmtString, s.testName, "\x1b[33mFailed, Retrying\x1b[0m" );

    s.testName = "V3: Goto checkpoint index 4";
    s.gotoCheckpoint( 4, false );
    if ( !testResultMsg( s.testName, s.lastResult()==0) ) return 1;

    s.testName = "V3: List checkpoints: list received";
    s.listCheckpoints( 0, 400 );
    if ( !testResultMsg( s.testName, s.lastResult() == 0) ) return 1;

    s.testName = "V3: List checkpoints: oldest index is 1";
    oldestCheckpointMarkIndex = atoi(checkpointList[checkpointListLen-1]);
    if ( !testResultMsg( s.testName, oldestCheckpointMarkIndex==1) ) return 1;

  }

  s.testName = "V3: Disconnect";
  s.disconnect();
  if ( !testResultMsg( s.testName, s.lastResult() == 0 ) ) return 1;


  // Second test:

  // 1. Open a journaled file (adds a checkpoint marker automatically)
  // 2. Add another checkpoint mark
  // 3. List checkpoints and remember the data of the second newest one in the list.
  // 4. List checkpoints again, but specify a starting offset of 1.
  // 5. Compare the newest tag in that list to the second newest in the first list.  They should be the same.

  s = Session(3);

  s.testName = "V3: Connect to journaled image";
  s.connect("rw_journaled.dsk");
  if ( !testResultMsg( s.testName, s.lastResult()==0 ) ) return 1;

  // Give checkpoint list scan some time to run
  sleep(1);

  s.testName = "V3: List checkpoints: receive a list";
  s.listCheckpoints( 0, 400 );
  if ( !testResultMsg( s.testName, s.lastResult()==0) ) return 1;

  s.testName = "V3: List has at least two entries";
  if ( !testResultMsg( s.testName, checkpointListLen > 1 ) ) return 1;

  char secondTagData[100];
  strcpy( secondTagData, checkpointList[1] );

  s.testName = "V3: List checkpoints: receive a list startLine = 1";
  s.listCheckpoints( 1, 400 );
  if ( !testResultMsg( s.testName, s.lastResult()==0) ) return 1;

  s.testName = "V3: List checkpoints: expected list received";
  if ( !testResultMsg( s.testName, strcmp(secondTagData, checkpointList[0]) == 0 ) ) return 1;

  s.testName = "V3: Disconnect";
  s.disconnect();
  if ( !testResultMsg( s.testName, s.lastResult() == 0 ) ) return 1;

 
  return 0;
}



int singleOpenTests( void ) {

  puts("Single client image open tests");

  Session s = Session(1);

  // A read-only image should have a flag that says it is read-only.

  s.testName = "V1: Read-only image connect, read-only flag set";
  s.connect("ro.dsk");
  bool success = (s.lastResult()==0) && (s.response.getPayloadByte( 4 ) == 0x01);
  if ( !testResultMsg( s.testName, success ) ) return 1;

  s.testName = "V1: Read-only image connect, image size = 1MB";
  int detectedSize = ((Payload_Connect_Response_t *)(s.response.getPayloadStart()))->imageSize;
  if ( !testResultMsg( s.testName, detectedSize == (1024*1024) ) ) return 1;

  s.testName = "V1: Disconnect";
  s.disconnect();
  if ( !testResultMsg( s.testName, s.lastResult()==0 ) ) return 1;


  // A read-write image should not have the read-only flag set.

  s.testName = "V1: Read-write image connect, read-only flag not set";
  s.connect("rw.dsk");
  success = (s.lastResult()==0) && (s.response.getPayloadByte( 4 ) == 0x00);
  if ( !testResultMsg( s.testName, success) ) return 1;

  s.testName = "V1: Disconnect";
  s.disconnect();
  if ( !testResultMsg( s.testName, s.lastResult()==0 ) ) return 1;


  // A read-write journaled image should not have the read-only flag set.

  s.testName = "V1: RW Journaled image connect, read-only flag not set";
  s.connect("rw_journaled.dsk");
  success = (s.lastResult()==0) && (s.response.getPayloadByte(4)==0x00);
  if ( !testResultMsg( s.testName, success) ) return 1;

  s.testName = "V1: Disconnect";
  s.disconnect();
  if ( !testResultMsg( s.testName, s.lastResult()==0 ) ) return 1;


  // A read-only journaled image should not have the read-only flag set.

  s.testName = "V1: RO journaled image connect, read-only flag not set";
  s.connect("ro_journaled.dsk");
  success = (s.lastResult()==0) && (s.response.getPayloadByte(4)==0x00);
  if ( !testResultMsg( s.testName, success) ) return 1;

  s.testName = "V1: Disconnect";
  s.disconnect();
  if ( !testResultMsg( s.testName, s.lastResult()==0 ) ) return 1;


  // A read-write session scoped image should not have the read-only flag set.

  s.testName = "V1: RW Session scoped image connect, session-scoped set, RO not set";
  s.connect("rw_ss.dsk");
  success = (s.lastResult()==0) && (s.response.getPayloadByte(4)==0x02);
  if ( !testResultMsg( s.testName, success) ) return 1;

  s.testName = "V1: Disconnect";
  s.disconnect();
  if ( !testResultMsg( s.testName, s.lastResult()==0 ) ) return 1;


  // A read-only session scoped image should not have the read-only flag set.

  s.testName = "V1: RO Session-scoped image connect, session-scoped set, RO not set";
  s.connect("ro_ss.dsk");
  success = (s.lastResult()==0) && (s.response.getPayloadByte(4)==0x02);
  if ( !testResultMsg( s.testName, success) ) return 1;

  s.testName = "V1: Disconnect";
  s.disconnect();
  if ( !testResultMsg( s.testName, s.lastResult()==0 ) ) return 1;

  return 0;
}


int multiOpenTests( void ) {

  puts("Multiple client open tests");

  Session s1 = Session(1);
  Session s2 = Session(1);

  // Read-Write image: Only one client is allowed at a time.

  s1.testName = "V1: Connect to read-write image";
  s1.connect("rw.dsk");
  if ( !testResultMsg( s1.testName, s1.lastResult()==0 ) ) return 1;

  s2.testName = "V1: Attempt a 2nd connection, should be rejected";
  s2.connect("rw.dsk");
  if ( !testResultMsg( s2.testName, s2.lastResult()==1 ) ) return 1;

  s1.testName = "V1: Disconnect first client";
  s1.disconnect();
  if ( !testResultMsg( s1.testName, s1.lastResult()==0 ) ) return 1;


  // Journaled image: Only one client is allowed at a time.

  s1.testName = "V1: Connect to journaled image";
  s1.connect("rw_journaled.dsk");
  if ( !testResultMsg( s1.testName, s1.lastResult()==0 ) ) return 1;

  s2.testName = "V1: Attempt a 2nd connection, should be rejected";
  s2.connect("rw_journaled.dsk");
  if ( !testResultMsg( s2.testName, s2.lastResult()==1 ) ) return 1;

  s1.testName = "V1: Disconnect first client";
  s1.disconnect();
  if ( !testResultMsg( s1.testName, s1.lastResult()==0 ) ) return 1;


  // Read-Only image: Multiple clients are allowed at a time.

  s1.testName = "V1: Connect to read-only image";
  s1.connect("ro.dsk");
  if ( !testResultMsg( s1.testName, s1.lastResult()==0 ) ) return 1;

  s2.testName = "V1: Attempt a 2nd connection, should be accepted";
  s2.connect("ro.dsk");
  if ( !testResultMsg( s2.testName, s2.lastResult()==0 ) ) return 1;

  s1.testName = "V1: Disconnect first client";
  s1.disconnect();
  if ( !testResultMsg( s1.testName, s1.lastResult()==0 ) ) return 1;

  s2.testName = "V1: Disconnect second client";
  s2.disconnect();
  if ( !testResultMsg( s2.testName, s2.lastResult()==0 ) ) return 1;


  // Session scoped image: Multiple clients are allowed at a time.

  s1.testName = "V1: Connect to session scoped image";
  s1.connect("rw_ss.dsk");
  if ( !testResultMsg( s1.testName, s1.lastResult()==0 ) ) return 1;

  s2.testName = "V1: Attempt a 2nd connection, should be accepted";
  s2.connect("rw_ss.dsk");
  if ( !testResultMsg( s2.testName, s2.lastResult()==0 ) ) return 1;

  s1.testName = "V1: Disconnect first client";
  s1.disconnect();
  if ( !testResultMsg( s1.testName, s1.lastResult()==0 ) ) return 1;

  s2.testName = "V1: Disconnect second client";
  s2.disconnect();
  if ( !testResultMsg( s2.testName, s2.lastResult()==0 ) ) return 1;


  // Can't open because both journaled and session scoped.
  s1.testName = "V1: Open image that is journaled and session-scoped, should be rejected";
  s1.connect("cant_open.dsk");
  if ( !testResultMsg( s1.testName, s1.lastResult()==1 ) ) return 1;

  return 0;
}



int miscTests( void ) {
  puts("Miscellaneous tests");
  Command c = buildConnectCommand(1, rand(), "bad_size.dsk");
  Command r = Command(1, 0, c.getSequence( ), Op_Connect, 1, 0, 0);
  if ( !executeTest("Image has a bad file size (not multiple of 512)", c, r) ) return 1;
  return 0;
}


int (*testGroups[])(void) = {
  protocolTests,
  recognizedCommandsTest,
  connectTests,
  imageLimitsTest,
  checkpointTests_v2,
  checkpointTests_v3,
  singleOpenTests,
  multiOpenTests,
  miscTests,
  NULL
};


int main(int argc, char **argv) {

  puts( "mTCP NetDrive protocol tests, (C)opyright 2024-2026 M. Brutman (mbbrutman@gmail.com)\n" );

  if ( argc < 3 ) {
    fprintf(stderr, "Not enough arguments!\n");
    fprintf(stderr, "Usage: %s <server_name> <server_port>\n", argv[0]);
    exit(1);
  }

  srand(time(NULL));

  struct addrinfo hints;

  memset(&hints, 0, sizeof(hints));
  hints.ai_family = AF_INET;
  hints.ai_socktype = SOCK_DGRAM;

  int gai_rc = getaddrinfo( argv[1], argv[2], &hints, &Server );
  if (gai_rc != 0 ) {
    fprintf(stderr, "getaddrinfo error: %s\n", gai_strerror(gai_rc));
    return -1;
  }

  Socket_fd = socket(Server->ai_family, Server->ai_socktype, Server->ai_protocol);
  if (Socket_fd < 0)
  {
    fprintf(stderr, "Error creating socket: %s\n", strerror(errno));
    return -1;
  }


  for ( int i=0; testGroups[i] != NULL; i++ ) {
    int rc = testGroups[i]( );
    if ( rc != 0 ) {
      puts( "Early exit due to failure." );
      break;
    }
  }

    
  printf("Finished...\n");

  return 0;
}
