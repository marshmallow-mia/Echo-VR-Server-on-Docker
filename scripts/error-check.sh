#!/bin/bash
#This script checks for errors and restarts the echo server instance
touch /scripts/test.log

#The script checks the following errors
errors=( "Unable to find MiniDumpWriteDump" "[NETGAME] Service status request failed: 400 Bad Request" "[NETGAME] Service status request failed: 404 Not Found" "[TCP CLIENT] [R14NETCLIENT] connection to ws:///login" "[TCP CLIENT] [R14NETCLIENT] connection to failed" "[TCP CLIENT] [R14NETCLIENT] connection to established" "[TCP CLIENT] [R14NETCLIENT] connection to closed" "[TCP CLIENT] [R14NETCLIENT] Lost connection (okay) to peer")

#The delay between checks
delayBetweenChecks=10 #Do not set lower then 10, otherwise it could start more then 1 instance because it could take to long to start the first one
#the time to wait before the script checks if the error is still there
timeToWaitBeforeRestart=30

#This function checks if the process is still running
function checkForRunningInstance {
    if ! [[ $(pgrep echovr.exe ) ]]
    then
        bash /scripts/start-echo.sh 
    fi
}


#this function checks for errors 
function checkForError {
    #get the last line of the error file
    lastLine=$(tail -1 /ready-at-dawn-echo-arena/logs/$HOSTNAME/*.log | cut -c 26- | sed -e s/"[0-9]*\.[0-9]*\.[0-9]*\.[0-9]*:[0-9]*"//g -e s#"ws://.* "##g -e s#" ws://.*api_key=.*"##g -e s/"\?auth=.*"//g)
echo "Post: "$lastLine >> /scripts/test.log
    #check the last line for any errors
    for error in "${errors[@]}"
    do
        #if an error was found
        if [[ "$error" =~ "$lastLine" ]]
		then
            #wait for the configured time before recheck
            sleep $timeToWaitBeforeRestart
            #get the last line again
            lastLineNew=$(tail -1 /ready-at-dawn-echo-arena/logs/$HOSTNAME/*.log | cut -c 26- | sed -e s/"[0-9]*\.[0-9]*\.[0-9]*\.[0-9]*:[0-9]*"//g -e s#"ws://.* "##g -e s#" ws://.*api_key=.*"##g -e s/"\?auth=.*"//g)
            #compare error line and current line
            if [[ "$lastLine" =~ "$lastLineNew" ]]
            then
                #kill the process and log the reason
                pkill "echovr"
                echo "Process killed. Reason: "$lastLine >> /ready-at-dawn-echo-arena/logs/$HOSTNAME/errorlog
            fi
        fi
    done
}


while :
do
#    checkForError
    checkForRunningInstance
    sleep $delayBetweenChecks
done


