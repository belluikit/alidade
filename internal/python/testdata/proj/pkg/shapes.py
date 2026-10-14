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


def make():
    return large


TABLE = {"s": small}


def dispatch(factory):
    make()(1)
    TABLE["s"](2)
    factory()(3)


dispatch(make)


class Held:
    def __enter__(self):
        return self

    def __exit__(self, *exc):
        return False


class Inherits(Held):
    pass


def holds(lock):
    with Held():
        pass
    with Inherits() as h, open(__file__):
        pass
    with lock:
        pass
    return sorted([3, 1], key=helper)


STUB = helper


class Box:
    @property
    def size(self):
        return 1


def measure(b):
    return b.size


import typing


def adder(n) -> typing.Callable[[int], int]:
    def add(x):
        return x + n
    return add


def plus_one(x):
    inc = adder(1)
    return inc(x)


from dataclasses import dataclass


@dataclass
class Handler:
    name: str
    run: typing.Callable[[int], int]


def twice(x):
    return 2 * x


HANDLERS = [Handler("double", twice), Handler(name="inc", run=plus_one)]


def run_all():
    for h in HANDLERS:
        h.run(1)


REGISTRY = []


def registered(fn):
    REGISTRY.append(fn)
    return fn


@registered
def triple(x):
    return 3 * x


def scaled(x):
    return double(x) + triple(x)
