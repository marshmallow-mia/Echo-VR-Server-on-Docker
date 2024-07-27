#!/bin/bash
delayToKillServer=1200 #in seconds
logPath="/ready-at-dawn-echo-arena/logs/$HOSTNAME/*.log"
waitingForChange=0


function checkForStuckServer {
    waitingForChange=1
    timeSinceStart=$(awk '{print $1}' /proc/uptime)
    lastLine=$(tail -1 $logPath)
    while :
    do
        if ! [ -f $logPath ]
        then
            echo "no file"
            waitingForChange=0
            return
        fi
        if ! [[ "$(tail -1 $logPath)" == "$lastLine" ]]
        then
            echo "different"
            waitingForChange=0
            return
        else
            totalTime=$(echo "$timeSinceStart + $delayToKillServer" | bc | awk '{print int($1)}')
            systemUptime=$(awk '{print int($1)}' /proc/uptime)

            if [[ $totalTime -le $systemUptime ]]
            then                
                if [[ "$(tail -1 $logPath)" == "$lastLine" ]]
                then
                    #kill the process and log the reason
                    pkill -f "echovr"
                    echo $(date)": Process killed. Reason: Stuck Server: " $lastLine >> /ready-at-dawn-echo-arena/logs/$HOSTNAME/errorlog
                    waitingForChange=0
                    return
                fi
            fi
            
        fi
        sleep 2
    done
    
}

while :
do
    echo $waitingForChange
    if [[ $waitingForChange -eq 0 ]]
    then
        checkForStuckServer
    fi
    sleep 2
done
