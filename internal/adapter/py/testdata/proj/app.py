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
