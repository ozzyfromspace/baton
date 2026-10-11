import datetime as dt
def P(s):
    s=s.replace('Z','').split('+')[0]
    if '.' in s:
        a,b=s.split('.'); s=a+'.'+(b+'000000')[:6]
    return dt.datetime.fromisoformat(s)
