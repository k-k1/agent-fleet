import json,sys,collections,datetime
js,log=sys.argv[1],sys.argv[2]
ev=[]
for l in open(js):
    d=json.loads(l)
    if 'Test' in d and '/' not in d['Test'] and d['Action'] in('run','pass','fail','skip'):
        t=datetime.datetime.fromisoformat(d['Time']).timestamp()
        ev.append((t,d['Action'],d['Test'],d.get('Elapsed',0)))
spans=[];cur={}
for t,a,n,e in ev:
    if a=='run': cur[n]=t
    else: spans.append((cur.pop(n,t),t,n,e))
spans.sort()
import bisect
starts=[s[0] for s in spans]
calls=collections.Counter();per=collections.defaultdict(collections.Counter);sub=collections.Counter()
for l in open(log):
    ts,src,k,sc=(l.split()+['']*4)[:4]; ts=float(ts)
    i=bisect.bisect_right(starts,ts)-1
    name=spans[i][2] if i>=0 and ts<=spans[i][1]+0.05 else '?'
    calls[(src,k)]+=1; per[name][src]+=1; sub[(src,sc)]+=1
tot=sum(calls.values())
print("total",tot)
for k,v in calls.most_common(): print(" ",k,v,f"{100*v/tot:.1f}%")
print("top subcommands:")
for k,v in sub.most_common(25): print(" ",k,v)
el={s[2]:s[3] for s in spans}
rows=sorted(per.items(),key=lambda x:-sum(x[1].values()))
print("tests with git calls:",len(rows),"of",len(spans))
print("top 20 tests by calls (prod/test, elapsed):")
for n,c in rows[:20]: print(f"  {sum(c.values()):5d} prod={c['prod']:5d} test={c['test']:4d} {el.get(n,0):5.2f}s {n}")
# zero-git tests time
z=[s for s in spans if s[2] not in per]
print("tests w/o git:",len(z),"time",round(sum(s[3] for s in z),1))
# correlation calls vs elapsed
tc=sum(sum(c.values()) for c in per.values()); te=sum(el.get(n,0) for n in per)
print(f"time in git tests {te:.1f}s over {tc} calls = {1000*te/tc:.2f} ms/call (upper bound)")
