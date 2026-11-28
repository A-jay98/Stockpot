import { expect, test } from 'vitest'
import { listQty } from '../catalog/suggest'
import { brandOf, storeLook } from './brands'

test('well-known stores match by name words, others get initials and a steady colour', () => {
  expect(brandOf('Costco Burnaby')?.brand).toBe('Costco')
  expect(brandOf("Trader Joe's")?.brand).toBe("Trader Joe's")
  expect(brandOf('T&T Supermarket')?.brand).toBe('T&T')
  expect(brandOf('Superstore')?.brand).toBe('Real Canadian Superstore')
  expect(brandOf('Targets of opportunity')).toBeUndefined()
  expect(storeLook({ name: 'Lidl', color: null }).path).toBeTruthy()
  const corner = storeLook({ name: 'Corner shop', color: null })
  expect(corner.mono).toBe('CS')
  expect(corner.brand).toBeUndefined()
  expect(storeLook({ name: 'Corner shop', color: null }).bg).toBe(corner.bg)
  expect(storeLook({ name: 'Corner shop', color: '#123456' }).bg).toBe('#123456')
})

test('list amounts: packages as multiples, measures as amounts', () => {
  expect(listQty('1', 'bag')).toEqual({ times: null, unit: 'bag' })
  expect(listQty('2', 'bag')).toEqual({ times: '2×', unit: 'bag' })
  expect(listQty('3', 'ea')).toEqual({ times: '3×', unit: null })
  expect(listQty(null, null)).toEqual({ times: null, unit: null })
  expect(listQty('500', 'g')).toEqual({ times: null, unit: '500 g' })
  expect(listQty(null, 'l')).toEqual({ times: null, unit: null })
})
