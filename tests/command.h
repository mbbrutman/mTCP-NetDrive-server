
/* mTCP NetDrive protocol test (command.h)

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


#ifndef _COMMAND_H
#define _COMMAND_H


#include <stdint.h>


typedef enum {
  Op_not_valid  = 0,
  Op_Connect    = 1,
  Op_Disconnect = 2,
  Op_Read       = 3,
  Op_Write      = 4,
  Op_Write_V    = 5,
  Op_Mark_Chkp  = 6, // Optional in v3 and up
  Op_Goto_Chkp  = 7, // Optional in v3 and up
  Op_List_Chkp  = 8  // Optional in v3 and up
} Operation_t;





#define COMMAND_PAYLOAD_MAX_V1 (1200)
#define COMMAND_PAYLOAD_MAX_V2 (1200)
#define COMMAND_PAYLOAD_MAX_V3 (2048)

#pragma pack(1)
typedef struct {
  uint16_t    Version;
  uint16_t    Session;
  uint16_t    Sequence;
  uint8_t     Operation;
  uint8_t     Result;
  uint32_t    StartSec;
  uint16_t    SecCount;
} Command_header_v1;

typedef struct {
  uint16_t    Version;
  uint16_t    Session;
  uint16_t    Sequence;
  uint8_t     Operation;
  uint8_t     Result;
  uint32_t    StartSec;
  uint16_t    SecCount;
} Command_header_v2;

typedef struct {
  uint16_t    Version;
  uint16_t    Session;
  uint16_t    Sequence;
  uint8_t     Operation;
  uint8_t     Result;
  uint32_t    StartSec;
  uint16_t    SecCount;
  uint16_t    CmdExt;
} Command_header_v3;


typedef struct {
  Command_header_v1  hdr;
  uint8_t            payload[COMMAND_PAYLOAD_MAX_V1];
} Command_v1_t;

typedef struct {
  Command_header_v2  hdr;
  uint8_t            payload[COMMAND_PAYLOAD_MAX_V2];
} Command_v2_t;

typedef struct {
  Command_header_v3  hdr;
  uint8_t            payload[COMMAND_PAYLOAD_MAX_V3];
} Command_v3_t;


class Command {

  public:

    // Default ctor: just a place holder
    Command( ) { }

    // More useful: Sets most of the header fields.
    Command( uint16_t version_p, uint16_t session_p, uint16_t sequence_p,
             uint8_t op_p, uint8_t result_p, uint32_t start_p, uint16_t count_p);

    // Getters and Setters.
    uint16_t getVersion( void ) { return c.v1.hdr.Version; }

    uint16_t getSession( void ) { return c.v1.hdr.Session; }
    void     setSession( uint16_t s ) { c.v1.hdr.Session = s; }

    uint16_t getSequence( void ) { return c.v1.hdr.Sequence; }
    void     setSequence( uint16_t s ) { c.v1.hdr.Sequence = s; }

    uint16_t getOperation( void ) { return c.v1.hdr.Operation; }
    void     setOperation( uint16_t o ) { c.v1.hdr.Operation = o; }

    uint16_t getResult( void ) { return c.v1.hdr.Result; }
    void     setResult( uint16_t r ) { c.v1.hdr.Result = r; }

    uint16_t getStartSec( void ) { return c.v1.hdr.StartSec; }
    void     setStartSec( uint16_t s ) { c.v1.hdr.StartSec = s; }

    uint16_t getSecCount( void ) { return c.v1.hdr.SecCount; }
    void     setSecCount( uint16_t p ) { c.v1.hdr.SecCount = p; }

    // V3 only
    uint16_t getCmdExt( void ) { return c.v3.hdr.CmdExt; }
    void     setCmdExt( uint16_t e ) { c.v3.hdr.CmdExt = e; }


    // Metadata
    int      getHeaderLen( void );
    int      getPayloadLen( void ) { return payloadLen; }
    int      getTotalLen( void ) { return getHeaderLen( ) + payloadLen; }

    uint8_t *getPayloadStart( void ) { return ((uint8_t *)&c) + getHeaderLen( ); }
    uint8_t  getPayloadByte( int index ) { return (((uint8_t *)&c) + getHeaderLen( ))[index]; };
    void     setPayload( const uint8_t *payload, int payloadLen );

    void     setPayloadLen( uint16_t payloadLen_p ) { payloadLen = payloadLen_p; }

    void *   getDataStart( void ) { return &c; };


  private:

    int payloadLen;

    union {
      Command_v1_t v1;
      Command_v2_t v2;
      Command_v3_t v3;
    } c;

    static int PayloadMaxLen[];

};





typedef struct {
  uint8_t  macAddr[6];
  char     filename[256];   // Null terminated
} Payload_Connect_v1_t;


typedef struct {
  uint8_t  macAddr[6];
  uint16_t clientType;
  uint16_t blockSize;
  char     osString[40];    // Null terminated
  char     filename[256];   // Null terminated
} Payload_Connect_v2_t;



typedef struct {
  uint32_t imageSize;
  uint16_t flags;
} Payload_Connect_Response_t;




typedef struct {
  uint16_t startLine;
  uint16_t bufferSize;
} Payload_List_Chkp_v3_req_t;

typedef struct {
  uint8_t  moreData;
  uint16_t checkpointsReturned;
} Payload_List_Chkp_v3_resp_t;


#endif
