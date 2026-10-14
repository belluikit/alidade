import pkg.shapes as shapes
from pkg import Square


def total(items):
    return sum(i.area() for i in items)


def main():
    sq = Square(2)
    sq.area()
    shapes.helper(3)

    def inner():
        return main()

    print(total([sq]))
    return inner


class Big(shapes.Square):
    pass


class Tall(Square):
    pass


class Failure(ValueError):
    pass


def kind(base):
    class Made(base):
        pass
    return Made


kind(Big)


def width():
    return shapes.Box().size.bit_length()


def sized():
    return shapes.measure(shapes.Box())
