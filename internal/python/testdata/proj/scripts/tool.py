import os
import sys

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "lib"))
import helperlib  # noqa: E402

helperlib.go()

NOTE = "——"; from helperlib import go as go2  # noqa: E402,E702
NOTE2 = "——"; helperlib.go()  # noqa: E702
