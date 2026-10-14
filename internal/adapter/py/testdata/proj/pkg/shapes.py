import functools

UNIT = 1


class Shape:
    sides = 0

    def area(self):
        return 0


class Square(Shape):
    sides = 4

    def __init__(self, side):
        self.side = side

    def area(self):
        return self.side * helper(self.side)


def helper(x):
    return x


def logged(fn):
    @functools.wraps(fn)
    def wrapper(*a):
        return fn(*a)
    return wrapper


@logged
def double(x):
    return helper(x) * 2


def small(x):
    return x


def large(x):
    return x


pick = small if UNIT else large
pick(1)
