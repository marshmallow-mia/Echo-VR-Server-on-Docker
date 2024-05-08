#!/bin/bash
#Only used as the entrypoint
bash /scripts/checkForStuckServer.sh &
bash /scripts/error-check.sh
#this is for running this script infinitely, so the container doesnt exit
tail -f /dev/null
