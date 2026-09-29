#!/bin/bash
# Test script that prints lines with occasional delays
echo "line 1: starting up"
echo "line 2: doing work"
sleep 0.1
echo "line 3: after short pause"
echo "line 4: more output"
sleep 0.2
echo "line 5: after longer pause"
echo "line 6: stderr message" >&2
echo "line 7: almost done"
sleep 0.1
echo "line 8: final line"
