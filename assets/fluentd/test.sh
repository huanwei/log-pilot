#!/bin/bash



TXT_FILE=./test3.txt



if [ -f "$TXT_FILE" ]; then
    exit
fi

touchfile(){
touch ./test2.txt
}

touchfile


