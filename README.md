# mTCP NetDrive server

Home page: http://www.brutman.com/mTCP/mTCP.html

## Welcome to the mTCP NetDrive server source code!

This is the official github repository for the mTCP NetDrive server. While I prefer to distribute the source code as a ZIP file, I also recognize that people are using github for their projects and they want a github repository to reference or fork from so that they can keep up with changes.

## Directory structure

At the moment there are just two directories:

* `server`: The mTCP NetDrive server, written in Go (Golang).
* `tests`: Some automated testcases written in C/C++ to test the server against the [mTCP NetDrive protocol specification](http://www.brutman.com/mTCP/Netdrive_protocol). The testcases check for minimal compliance with the protocol specification; I'll be adding more testcases of various types as time goes on.

## Compiling and building

The server is a Golang program.  It can be built by running the `build.sh` script in the server directory.  That script makes it easy for me to inject a build date or other custom string into the program. The normal `go build` process works as well  but it doesn't inject the build date.

The testcases are in a C/C++ program. Also provided is a binary blob representing a small hard drive image and a script that uses the blob to setup a test environment for the testcases. See the readme file in that directory for the instructions on building it and running it.

## Documentation

See the PDF file in each release for the documentation on how to run the mTCP NetDrive server.

## Support

I want you to have a great experience with mTCP and I provide support via email.  Please send your questions, bug reports and feature requests to me to me at the email address listed above.  There is also a low-volume mailing list you can subscribe to at https://groups.google.com/g/mtcp. And of course, there is the github issue tracker.

If you are running an older version of mTCP please try the latest version first before contacting me; your problem might be a bug that has already been fixed.

Don't understand the documentation?  That might be my fault.  Send me an email with your question and I'll try to make it more clear.

Can't make it work?  There could be lots of reasons.  I try to explain the common problems in the PDF documentation. If you have something that is truly uncommon and you are tired of banging your head on the wall send me an email. There might be a bug or you might just need a gentle prod in the right direction.

 If you do ask for help via email please send me enough detail so that I understand what you are seeing and why it is a problem.

## My views on AI

This is my hobby project, and I do this for fun. As a result, I insist on doing my own work and I do not use AI
for any part of this project.

## Like what you see?  Want to say thanks?

Thank you!  I have never accepted any money, beer or cookies for my work. If you want to say thanks in a tangible way
I've never seen an animal shelter that was over-funded.

I also have mTCP stickers to hand out if you ever see me in person.

## Questions or comments? Please email me!

Mike

