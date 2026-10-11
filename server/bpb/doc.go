/*
Package bpb implements a minimal BIOS Parameter Block structure
that is used for basic error checking on images that are requested
by clients.

It only implements enough of the BPB to do basic error checking.
It does not attempt to implement all of the BPB extensions that have
been added over the years; DOS will look at those fields if it needs
to.

# Terminology

* A partitioned device has a master boot record.  The master boot
  record has a partition table and some boot code.

* A partition also has a boot record, known as a volume boot record.
  On a FAT partition this boot record has a BIOS Parameter Block
  data structure and some boot code.

* When we talk about serving an image we are actually talking about
  serving a partition image, not a hard drive image.  So we are always
  dealing with volume boot records.

* When this code expands to support other systems none of this will
  matter, as it will just want to read and write 512 byte blocks and
  not try to interpret them.  This code is here now because it only
  supports DOS and it is trying to be helpful by doing some extra
  error checking.

# BPB description

The BPB is a data structure embedded in the boot sector (volume boot
record) of a FAT partition.  It describes the partition and the layout
of the FAT filesystem on the partition.

The BPB was introduced in DOS 2.0 and it has been extended several times
over the years.  I do not model all of the extensions here as they are
not needed for the device driver.  They may be present in the images
that we serve but the device driver doesn't need the extra fields.

The rules for handling these fields can get complicated because of the
extensions and remapping of the fields over the years.  The good news
is that we really don't care about the corner cases - we are just passing
this data to DOS and DOS can do what it needs to with it.  We do some
basic error checking so there might be some images out there that we
don't handle correctly.

DOS 1.x doesn't use a BPB at all so when we detect an obviously invalid
BPB we check to see if the image is one of the official DOS 1.x supported
types.  This is based on the MediaDescriptor byte in the FAT structure
and the image size.

If the server gets extended to other filesystems then the BPB code should
be bypassed.

Some misc notes:

* HiddenSectors is used by partitioned devices and is the count of
  sectors before this boot record.  It was introduced in DOS 3.0 as a uint16.
  It should always be 0 for our images, as we are serving images of partitions,
  not partitioned hard drives.

* DOS 3.2 introduced HugeSectors following HiddenSectors, where HugeSectors
  included HiddenSectors in its count.  This was also a uint16.

* DOS 3.31 basically changes HiddenSectors and HugeSectors to be uint32s.
  Of course this breaks the existing BPB.  To get around this the NumSectors
  field is set to 0, denoting that HiddenSectors and HugeSectors are uint32s.

*/
package bpb

