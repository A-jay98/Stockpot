import { describe, expect, it } from 'vitest'
import fixture from '../../../internal/units/testdata/convert.json'
import { type Conversions, convert, format, rat } from './units'

type Case = {
  name: string
  qty: string
  from: string
  to: string
  conversions?: Conversions[]
  want: string | null
}

describe('convert (shared fixture with internal/units)', () => {
  for (const c of fixture.cases as Case[]) {
    it(c.name, () => {
      const q = rat(c.qty)
      expect(q).not.toBeNull()
      const got = q && convert(q, c.from, c.to, ...(c.conversions ?? []))
      expect(got ? format(got) : null).toBe(c.want)
    })
  }
})
