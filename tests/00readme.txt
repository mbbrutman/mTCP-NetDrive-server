
Readme for netdrive_test
2026-10-06


This is a series of short test cases to run against the mTCP NetDrive
server with a predefined set of test images to ensure that some very
basic operations work as expected.

More complex testing probably should be done using a real NetDrive
client.  (Example: I use a disk exerciser under DOS for stress
testing.)


Compiling:

    g++ netdrive_test.cpp command.cpp session.cpp -o netdrive_test

  Yes, it's that simple!


Running:

  (1) Use the create_test_env.sh script to create a test image directory.
  (2) Run the netdrive server with the -image_dir flag set to that test image directory.
  (3) Run this program against that netdrive server:

      netdrive_test <server_ip_addr> <server_port>

      Example: netdrive_test calculon 2002

  Examine the output - green is good, an occasional yellow is a retry,
  and red is bad.  (The retry may happen when running the list checkpoints
  test; previous state may need to be cleared to get it to pass.  The retry
  process handles that.)

  Some UDP parts stolen from https://beej.us/guide/bgnet/examples/talker.c

  There is one test (a version number) test which is expected to fail.
  See the comments in the code for the reason why.


Philosphy:

  Writing a software specification is hard.  Writing software that meets
  that specification is hard too.

  Right now we have:

  * A server written in Go.
  * A DOS command line utility to manage connections written in C.
  * A DOS device driver written in assembly.
  * A specification that tries to document the protocol.

  This code is an attempt to find holes in the protocol or the server
  implementation.  It provides a framework that allows one to send
  commands (valid and invalid) to probe the server response.  That allows
  for a level of testing that the DOS command line utility and device
  driver can't do.  The code runs in Unix-like environments to provide
  some diversity and hopefully poke at some corner cases, like endian-ness.

