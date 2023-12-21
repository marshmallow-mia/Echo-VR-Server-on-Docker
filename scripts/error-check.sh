#!/bin/bash
#This script checks for errors and restarts the echo server instance

#The script checks the following errors
errors=( "Unable to find MiniDumpWriteDump" "[TCP CLIENT] [R14NETCLIENT] connection to ws:///config closed" "[NETGAME] Service status request failed: 400 Bad Request" "[NETGAME] Service status request failed: 404 Not Found" "[TCP CLIENT] [R14NETCLIENT] connection to ws:///login" )

#The delay between checks
delayBetweenChecks=2
#the time to wait before the script checks if the error is still there
timeToWaitBeforeRestart=30

#This function checks if the process is still running
function checkForRunningInstance {
    #need to check for lower then 2, as the grep itself gets found as well....
    if [ $( ps -aux | grep echovr.exe -c ) -lt 2 ]
    then
        bash /scripts/start-echo.sh 
    fi
}


#this function checks for errors 
function checkForError {
    #get the last line of the error file
    lastLine=$(tail -1 /ready-at-dawn-echo-arena/logs/$HOSTNAME/*.log | cut -c 26- | sed -e s/"[0-9]*\.[0-9]*\.[0-9]*\.[0-9]*:[0-9]*"//g -e s/"\?auth=.*"//g)
    #check the last line for any errors
    for error in "${errors[@]}"
    do
        #if an error was found
        if [ "$error" == "$lastLine" ]
        then
            #wait for the configured time before recheck
            sleep $timeToWaitBeforeRestart
            #get the last line again
            lastLineNew=$(tail -1 /ready-at-dawn-echo-arena/logs/$HOSTNAME/*.log | cut -c 26- | sed -e s/"[0-9]*\.[0-9]*\.[0-9]*\.[0-9]*:[0-9]*"//g -e s/"\?auth=.*"//g)
            #compare error line and current line
            if [ "$lastLine" == "$lastLineNew" ]
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
    checkForError
    checkForRunningInstance
    sleep $delayBetweenChecks
done


