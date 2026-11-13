import { describe, expect, it } from 'vitest'
import { MAX_PASS, decideRedirect, passFromFragment, shownName, takeCredentials } from './identityPass'

// The pass in the fragment (docs/plans/decide-grant.md, "web/app"): what is taken, what is left in the address, and where the
// decide middleware sends a navigation.

const TOKEN = 'abcdefghijklmnop0123456789'
const pass = (n: number) => 'a.b-c_'.repeat(Math.ceil(n / 6)).slice(0, n)

describe('passFromFragment', () => {
  it('takes a pass of MAX_PASS characters with dots and refuses one longer or of another character', () => {
    expect(MAX_PASS).toBe(2048)
    expect(passFromFragment(`#pass=${pass(2048)}`)).toBe(pass(2048))
    expect(passFromFragment(`#other=1&pass=${pass(2048)}`)).toBe(pass(2048))
    expect(passFromFragment(`#pass=${pass(2049)}`)).toBe('')
    expect(passFromFragment('#pass=abc%2Fdef')).toBe('')
    expect(passFromFragment('#pass=ab+cd')).toBe('')
    expect(passFromFragment('#pass=')).toBe('')
    expect(passFromFragment('')).toBe('')
  })
})

describe('takeCredentials', () => {
  it('takes both keys out of the fragment and keeps the rest', () => {
    expect(takeCredentials(`http://h.test/decide/dr-7#pass=p.1&x=2&token=${TOKEN}`)).toEqual({ token: TOKEN, pass: 'p.1', href: 'http://h.test/decide/dr-7#x=2' })
    expect(takeCredentials('http://h.test/decide#pass=p.1')).toEqual({ token: '', pass: 'p.1', href: 'http://h.test/decide' })
  })
  it('drops a refused value from the address too', () => {
    expect(takeCredentials('http://h.test/decide#pass=a%2Fb&token=short')).toEqual({ token: '', pass: '', href: 'http://h.test/decide' })
  })
  it('leaves a fragment without either key as it is', () => {
    expect(takeCredentials('http://h.test/files#section').href).toBe('http://h.test/files#section')
    expect(takeCredentials('http://h.test/files').href).toBe('http://h.test/files')
  })
})

describe('decideRedirect', () => {
  it('opens the operator page for a decide address while a workbench token is held, with or without a pass', () => {
    for (const hasPass of [false, true]) {
      expect(decideRedirect('/decide/dr-7', true, hasPass)).toBe('/decisions?item=decision:dr-7')
      expect(decideRedirect('/decide', true, hasPass)).toBe('/decisions')
      expect(decideRedirect('/decisions', true, hasPass)).toBeNull()
      expect(decideRedirect('/runs/r1', true, hasPass)).toBeNull()
    }
  })
  it('keeps a tab that holds a pass alone on the decide page, a decision named by the operator page opening there', () => {
    expect(decideRedirect('/decide', false, true)).toBeNull()
    expect(decideRedirect('/decide/dr-7', false, true)).toBeNull()
    expect(decideRedirect('/decisions?item=decision:dr-7', false, true)).toBe('/decide/dr-7')
    expect(decideRedirect('/decisions?item=decision%3Adr-7', false, true)).toBe('/decide/dr-7')
    expect(decideRedirect('/decisions?item=session:s1', false, true)).toBe('/decide')
    expect(decideRedirect('/decisions?item=decision:nope', false, true)).toBe('/decide')
    for (const p of ['/', '/decisions', '/runs/r1', '/settings', '/join/abc']) expect(decideRedirect(p, false, true)).toBe('/decide')
  })
  it('sends nowhere with neither', () => {
    for (const p of ['/', '/decide', '/decide/dr-7', '/decisions?item=decision:dr-7']) expect(decideRedirect(p, false, false)).toBeNull()
  })
  it("judges the first navigation's fragment as held, and takes the keys off the address", () => {
    expect(decideRedirect('/decide/dr-7#pass=p.1', false, false)).toBe('/decide/dr-7')
    expect(decideRedirect('/decisions?item=decision:dr-7#pass=p.1&x=1', false, false)).toBe('/decide/dr-7#x=1')
    expect(decideRedirect(`/decide/dr-7#pass=p.1&token=${TOKEN}`, false, false)).toBe('/decisions?item=decision:dr-7')
    expect(decideRedirect(`/wall#token=${TOKEN}`, false, false)).toBe('/wall')
    expect(decideRedirect('/wall#token=short&x=1', false, false)).toBe('/wall#x=1')
    expect(decideRedirect('/files#section', false, false)).toBeNull()
  })
})

describe('shownName', () => {
  it('is the name as an answer is shown under it: format and line characters dropped, spaces trimmed, at most 64', () => {
    expect(shownName('Ana', 's1')).toBe('Ana')
    expect(shownName('​ A‮na   ', 's1')).toBe('Ana')
    expect(shownName('A\tn\na\u0007', 's1')).toBe('A n a')
    expect(shownName('\u0085Ana　', 's1')).toBe('Ana')
    expect(shownName('é'.repeat(70), 's1')).toBe('é'.repeat(64))
    expect(shownName('😀'.repeat(65), 's1')).toBe('😀'.repeat(64))
  })
  it('is the actor when nothing is left', () => {
    expect(shownName('​  ', 'ana-7')).toBe('user:ana-7')
    expect(shownName(undefined, 'ana-7')).toBe('user:ana-7')
  })
})
