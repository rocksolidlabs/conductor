// A test provider module (docs/plans/phone-shell.md, Tests per surface, the boot checks' decision case): the tests' own, naming no
// service. It registers with the shell's bridge as a packaging's module does, reports this phone signed in, and holds one item whose
// decision's context carries a fenced go block and a Mermaid diagram, so the boot check opens the decision page under the shell's
// policy. It uses neither the key nor the store it is handed (in a browser they refuse every call).
/* global conductorShell */
conductorShell.register(function () {
  var rows = [
    { step: 'available', state: 'done', words: 'Request available' },
    { step: 'recorded', state: 'waiting', words: 'No answer yet' },
    { step: 'validated', state: 'waiting', words: '' },
    { step: 'resumed', state: 'waiting', words: '' },
  ]
  var item = {
    id: 'boot-1',
    decision: {
      id: 'dr-1', run: 'boot-0000aaaa', openedIn: 'boot-0000aaaa', origin: 'agent', type: 'approval', policy: {}, state: 'open', rev: 1,
      createdAt: '2026-11-19T09:00:00Z',
      request: {
        question: 'May the boot check pass?',
        context: 'The change:\n\n```go\npackage main\n\nfunc main() { println("boot") }\n```\n\n```mermaid\ngraph TD\n  Ask --> Answer\n```\n',
        options: [{ label: 'Approve' }, { label: 'Reject' }],
      },
    },
    rows: rows,
    owner: { name: 'Boot desk' },
    asOf: '2026-11-19T09:00:00Z',
    offline: false,
  }
  var state = { signedIn: true, who: 'boot@example.test', notifications: 'on' }
  return {
    name: 'Boot provider',
    state: function () { return Promise.resolve(state) },
    onState: function () { return function () {} },
    signIn: function () { return Promise.resolve({ words: 'Signed in already' }) },
    cancelSignIn: function () { return Promise.resolve() },
    signOut: function () { return Promise.resolve() },
    enableNotifications: function () { return Promise.resolve('on') },
    test: function () { return Promise.resolve({ words: 'Sent' }) },
    route: function () { return Promise.resolve(null) },
    inbox: {
      list: function () { return Promise.resolve({ items: [item], asOf: item.asOf }) },
      item: function () { return Promise.resolve(JSON.parse(JSON.stringify(item))) },
      respond: function () { return Promise.resolve({ outcome: 'refused', code: 'not_offered', words: 'The boot check answers nothing' }) },
      answerOf: function () { return Promise.resolve({ state: 'none' }) },
      resolve: function () { return Promise.resolve({ id: item.id }) },
      watch: function () { return function () {} },
    },
    drafts: {
      get: function () { return Promise.resolve(null) },
      list: function () { return Promise.resolve([]) },
      put: function () { return Promise.resolve({ ok: false, code: 'not_saved', words: 'The boot check keeps no drafts' }) },
      remove: function () { return Promise.resolve() },
    },
  }
})
