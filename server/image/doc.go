/*
Package image implements the code for reading and writing to images and
journaled images and maintaining image journals.

In general, this code just reads and writes 512 byte blocks of data on
behalf of a remote client.  That part is pretty simple.

# Images and Journaled Images

The optional journaling support allows one to undo recent writes.  It
is used in two ways:

* Session scoped writes: All of the writes from a particular client
  go to a journal that is private to that client.  The journal only
  lasts for the duration of the session and it is thrown away without
  committing the writes at the end of the session.  This allows several
  clients to use the same image concurrently and have the illusion that
  they are able to write to it, without having to clone the image for
  each client.

  This is useful as a "demo" mode; clients can try things out and mess
  up their view of the image in what is effectively a private sandbox.

* Undo support on an image: In this mode the journal for the image is
  persistent across client sessions, just like with a normal image.
  However you can implement undo by truncating the journal so that all
  of the writes past the point of truncation are thrown away, making it
  look like those writes never happened.

  In this usage the image and the journal are a linked pair.  At some
  point the journal will get too long and you will need to commit some
  or all of the writes to the image to and rewrite the journal to get
  it down to a manageable size.

If a journal is active all writes get appended to the journal.  To ensure
the client sees the correct data, all reads must check the journal
first and return the newest version of a block found in the journal.
If the block is not present in the journal then it has not been altered
and it is read from the base image.

Besides taking all new writes persistent journals also may have
"checkpoint markers", which is just a metadata block that denotes a
safe place to truncate the journal at.  In theory one can truncate the
journal anywhere, but in practice that is dangerous because it can leave
the filesystem in an inconsistent state.  Checkpoint markers get around
that problem, but also limit the ability to truncate at any point.
An advanced tool could allow for truncation at arbitrary points in the
journal, but it would be responsible for choosing points that do not
leave the filesystem in an inconsistent state.

# Code organization

This package has code to handle two major use cases:

* Opening, closing and reading/writing images and journals.  This is
  primarily used by the server on behalf of connected clients.
	(These are "online" operations.)
* Performing maintenance on journals.  Maintenance operations are done
  "offline" using the command line interface.

# Offline journal maintenance functions

The following functions are designed to be invoked from the command line:

* Compaction: Rewrite a journal to eliminate out-of-date blocks that
  can not be retreived by a client.
* Committing: Merge the writes in the journal into the base image,
  making them permanent in the image.
* Compare two images (including their journals).  This probably isn't
  needed but if you want to make a backup of an image and its journal
  before compacting or committing them then comparing can be used to
  ensure those operations worked correctly

In theory compaction and commiting can happen online but that requires
pausing a connected client, which is not implemented yet.

# Error reporting

Errors returned to callers of this package should be short and safe to pass
all of the way to the end user.  Keep the low-level implementation details
out of the error message.   Don't pass an error you received from the
runtime directly back to the user, as it probably has too much detail in it.

If you want to capture stuff for debugging, use the logging (mlog) facility.

I thought about using error wrapping here but the logging facility is
better for that kind of debugging.  You can wrap errors going to callers
in this package if you want to, but it's not really necessary.

Most errors can be reported to the end user.  For errors that are fatal
to a session use globals.FatalError.

*/
package image
