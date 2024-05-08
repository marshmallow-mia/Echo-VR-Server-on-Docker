#!/bin/bash
#Only used as the entrypoint
bash /scripts/error-check.sh &
bash /scripts/checkForStuckServer.sh &
#this is for running this script infinitely, so the container doesnt exit
tail -f /dev/null
