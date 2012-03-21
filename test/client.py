#!/usr/bin/env python
import sys
from twisted.python import log
from twisted.internet import reactor
from autobahn.fuzzing import FuzzingClientFactory

if __name__ == '__main__':
   log.startLogging(sys.stdout)
   spec = {
       "options": {"failByDrop": False},
       "enable-ssl": False,
       "servers": [
           {"agent": "gows/r", "url": "ws://localhost:9000/r", "options": {"version": 17}},
           {"agent": "gows/c", "url": "ws://localhost:9000/c", "options": {"version": 17}},
        ],
       "cases": ["*"],
       "exclude-cases": [],
       "exclude-agent-cases": {},
       }
   fuzzer = FuzzingClientFactory(spec)
   reactor.run()
