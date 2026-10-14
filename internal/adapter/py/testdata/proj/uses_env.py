import sys

sys.path.insert(0, "env/lib/site-packages")
import vendored  # noqa: E402

vendored.vend()


class Mine(vendored.Vended):
    pass
