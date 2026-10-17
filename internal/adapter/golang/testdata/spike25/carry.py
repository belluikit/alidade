import os, sys, collections
src=open('run.py').read().replace('\nmain()\n','\n'); ns={'__file__':os.path.abspath('run.py')}; exec(src,ns)
pkg=ns['pkg']
def carry(base, wts):
    bn,be=ns['read'](os.path.basename(base)+'.base.tsv')
    for wt in wts:
        P,_=ns['changed_pkgs'](base, wt); name=os.path.basename(wt)
        on,oe=ns['read'](name+'.oracle.tsv'); an,ae=ns['read'](name+'.approx.tsv')
        n,e=ns['splice'](bn,be,an,ae,P)
        # carry: base whole-program edges from a declaration the overlay still has, into an unchanged package
        c={x for x in be if pkg(x[0]) in P and pkg(x[1]) not in P and x[2] in ('calls','implements') and x[0] in n and x[1] in n} - e
        e2=e|c
        fromP=sum(1 for x in oe if pkg(x[0]) in P)
        inP=lambda s: [x for x in s if pkg(x[0]) in P]
        m1,m2=inP(oe-e),inP(oe-e2); s2=inP(e2-oe)
        print('%-14s fromP %6d | approx missing %4d (%.2f%%) | +carried %4d: missing %4d (%.2f%%), stale %4d (%.2f%%)' % (name, fromP, len(m1), 100*len(m1)/fromP, len(c), len(m2), 100*len(m2)/fromP, len(s2), 100*len(s2)/fromP))
carry(sys.argv[1], sys.argv[2:])
