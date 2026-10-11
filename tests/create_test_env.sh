#!/bin/bash
#
# This script is used to setup a testing environment for mTCP NetDrive.
#
# (1) Run this script to create a temp directory with a few test images in it.
# (2) Run the netdrive server using the temp directory as the image directory.)
# (3) Run the test program. (netdrive_test)
# (4) If everything is good you can reuse the temp directory or delete it.
# (5) If a test fails and corrupts the temp directory just flush it and make a new one.

tempdir=`mktemp -d`
echo "Temporary directory is: ${tempdir}"

# Create a small FAT12 disk image and some variations of it.

echo "Creating test images and setting directory permissions."
dd if=/dev/zero of=${tempdir}/ro.dsk bs=1024 count=1024
dd conv=notrunc if=test_data.bin of=${tempdir}/ro.dsk

pushd ${tempdir} > /dev/null

cp ro.dsk ro_journaled.dsk
touch ro_journaled.dsk.journal
cp ro.dsk ro_ss.dsk
touch ro_ss.dsk.session_scoped
cp ro.dsk rw.dsk
cp rw.dsk rw_journaled.dsk
touch rw_journaled.dsk.journal
cp rw.dsk rw_ss.dsk
touch rw_ss.dsk.session_scoped
cp rw.dsk cant_open.dsk
touch cant_open.dsk.journal
touch cant_open.dsk.session_scoped
dd if=ro.dsk of=bad_size.dsk bs=256 count=101
chmod 550 ro*.dsk
chmod 770 rw*.dsk

popd > /dev/null

echo ""
echo "The test files are ready."
echo ""
echo "Use netdrive serve -image_dir ${tempdir} and run the test cases."
echo "Don't forget to remove ${tempdir} when you are done."
