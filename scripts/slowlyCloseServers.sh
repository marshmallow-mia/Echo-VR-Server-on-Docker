#!/bin/bash
echoPath="../ready-at-dawn-echo-arena"
containerName=$( grep dockerContainerName= ../run.sh | sed -e "s/dockerContainerName=\"//g" -e "s/\"//g" )
shutdownMessages=('[ECHORELAY.GAMESERVER] Signaling end of session'
'[NSLOBBY] registration successful'
'[NETGAME] NetGame switching state (from logged in, to lobby)'
'[TCP CLIENT] [R14NETCLIENT] connection to'
'[LEVELLOAD] Unloading level')


#This function slowly closes the server if no one is on it.
function slowlyCloseServers {
        #Save each Docker Container ID in an array
        mapfile -t runningContainer < <(docker ps --filter "ancestor=$containerName" --format "{{.ID}}")
        
        if ! [[ $secondRun ]]
        then
            #save the amount of running servers        
            declare -g amountRunningServer=${#runningContainer[@]}
            echo $amountRunningServer "Servers are running. We will slowly close them now."
            secondRun=1
        fi
        
        #check logs of each Container to close it.
        for id in ${runningContainer[*]}
        do
            toClose=0
            #get last 10 lines and check each one
            logContent=$(tail -5 $echoPath/logs/$id/*.log) 
            for message in "${shutdownMessages[@]}"
            do
                if echo "$logContent" | grep -q -F "$message"
                then
                    toClose=1
                fi
            done
            #if [[ $logContent =~ ${shutdownMessages[@]} ]]
            #then
            #    toClose=1
            #fi
            
            if [[ $toClose == 1 ]]
            then
                docker stop $id
            fi
            
        done
        
}

function startServer {
    a=0
    while [[ $a -lt $amountRunningServer ]]
    do
        bash ../run.sh
        ((a++))
        sleep 5
    done    
}

if [[ $1 == "--restart" ]]
then
        while [[ $( docker ps --filter "ancestor=$containerName" --format "{{.ID}}" | wc -l ) -gt 0 ]]
        do
                slowlyCloseServers
                sleep 1
                if [[ $( docker ps --filter "ancestor=$containerName" --format "{{.ID}}" | wc -l ) -eq 0 ]] 
                then
                       startServer 
                fi
        done
elif ! [[ $1 ]]
then
        while [[ $( docker ps --filter "ancestor=$containerName" --format "{{.ID}}" | wc -l ) -gt 0 ]]
        do
                slowlyCloseServers
                sleep 1
        done
elif [[ $1 == "--start" ]]
then
        startServer 
fi
