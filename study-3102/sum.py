import json,sys
tot=0;n=0;fail=0;tests=[]
for l in open(sys.argv[1]):
    d=json.loads(l)
    if 'Test' in d and '/' not in d['Test'] and d.get('Action') in ('pass','fail','skip'):
        tot+=d.get('Elapsed',0);n+=1;tests.append((d.get('Elapsed',0),d['Test']))
        if d['Action']=='fail': fail+=1
print(f"{sys.argv[1]}: tests={n} fail={fail} sum_elapsed={tot:.1f}s")
if len(sys.argv)>2:
    for e,t in sorted(tests,reverse=True)[:int(sys.argv[2])]: print(f"  {e:6.2f} {t}")
