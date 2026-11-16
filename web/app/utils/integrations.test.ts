import { describe, expect, it } from 'vitest'
import { noInstallText, reportStatus, snippetLeft } from './integrations'

describe('reportStatus', () => {
  const it0 = { installed: false, launchInjection: false, experimental: false }
  it('reports when the hooks are installed', () => {
    expect(reportStatus({ ...it0, installed: true })).toEqual({ key: 'reports', words: 'Reports to Conductor' })
    expect(reportStatus({ ...it0, installed: true, experimental: true }).key).toBe('reports')
  })
  it('reports when a launch injects the hooks, with nothing installed', () => {
    expect(reportStatus({ ...it0, launchInjection: true })).toEqual({ key: 'reports', words: 'Reports to Conductor' })
  })
  it('says an adapter in preview, not installed, is experimental', () => {
    expect(reportStatus({ ...it0, experimental: true })).toEqual({ key: 'experimental', words: 'Experimental' })
  })
  it('says an adapter neither installed nor injected is not set up', () => {
    expect(reportStatus(it0)).toEqual({ key: 'unset', words: 'Not set up' })
  })
  it('says an agent with no adapter has no hooks, and how it reports instead', () => {
    expect(reportStatus(undefined)).toEqual({ key: 'none', words: 'No hooks: it reports by the bell or a screen pattern' })
  })
})

describe('noInstallText', () => {
  it('says there is nothing to install for an adapter without a file, home or not', () => {
    expect(noInstallText({ installable: false, launchInjection: true }, false)).toMatch(/^Nothing to install: this server wires it at launch/)
    expect(noInstallText({ installable: false, launchInjection: false }, true)).toBe('Nothing to install: paste the snippet.')
  })
  it('blames the missing home only for an adapter that has a file', () => {
    expect(noInstallText({ installable: true, launchInjection: false }, false)).toMatch(/no home directory/)
  })
})

describe('snippetLeft', () => {
  it('opens the snippet when the reply to an install carries it', () => {
    expect(snippetLeft({ code: 'no_file_route', details: { snippet: '{"hooks":{}}' } })).toBe(true)
  })
  it('keeps it shut for a skill file left by hand, which comes without one, or for another error', () => {
    expect(snippetLeft({ code: 'no_file_route', details: {} })).toBe(false)
    expect(snippetLeft({ code: 'no_file_route', details: { snippet: '' } })).toBe(false)
    expect(snippetLeft({ code: 'install_failed', details: { snippet: 'x' } })).toBe(false)
  })
})
