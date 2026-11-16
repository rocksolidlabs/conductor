/**
 * Open on your phone, from any page: the dialog (OpenOnPhone.vue) is mounted once by the default layout, and the decision page's
 * button and your menu open it with `show()`.
 */
export function useOpenOnPhone() {
  const open = useState<boolean>('openOnPhone', () => false)
  return { open, show: () => (open.value = true) }
}
