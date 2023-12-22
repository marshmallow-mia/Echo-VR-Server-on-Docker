#!/bin/bash
#create a virtual Screen
Xvfb :0 -screen 0 1024x768x16 &
#Only used as the entrypoint
bash /scripts/error-check.sh
#this is for running this script infinitely, so the container doesnt exit
tail -f /dev/null
