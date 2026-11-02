# usage: shard.py <N> <timing.json> <testbin> <label>  -- run N concurrent -test.run shards, greedy-balanced by previous timings
import json,sys,subprocess,time,os
N=int(sys.argv[1]);tj=sys.argv[2];binp=sys.argv[3];label=sys.argv[4]
el={}
for l in open(tj):
    d=json.loads(l)
    if 'Test' in d and '/' not in d['Test'] and d['Action'] in('pass','fail','skip'): el[d['Test']]=d.get('Elapsed',0)
bins=[[0,[]] for _ in range(N)]
for t,e in sorted(el.items(),key=lambda x:-x[1]):
    b=min(bins,key=lambda b:b[0]); b[0]+=e; b[1].append(t)
cwd='/home/dev/repos/agent-fleet@feature-3102-memoryx-parallel-tests-study/workspace/agent/internal/memoryx'
l0=open('/proc/loadavg').read().strip(); s=time.time(); ps=[]
for i,(w,ts) in enumerate(bins):
    f=open(f'm/{label}.shard{i}.raw','w')
    ps.append(subprocess.Popen([binp,'-test.count=1','-test.timeout=20m','-test.v=test2json','-test.run','^('+'|'.join(ts)+')$'],cwd=cwd,stdout=f,stderr=subprocess.STDOUT))
rcs=[p.wait() for p in ps]; e=time.time(); l1=open('/proc/loadavg').read().strip()
npass=sum(open(f'm/{label}.shard{i}.raw').read().count('--- PASS') for i in range(N))
line=f"{label} N={N} rcs={rcs} wall={e-s:.1f} passes={npass} load_start=[{l0}] load_end=[{l1}]"
print(line); open('m/summary.txt','a').write(line+'\n')
