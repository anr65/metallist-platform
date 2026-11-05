// Keep PAN as text: leading zeroes and all digits must survive form submission.
export function cardNumberError(pan) {
  if (!/^[0-9]{16,19}$/.test(pan)) return 'Введите полный номер карты: от 16 до 19 цифр без пробелов.';
  let sum = 0;
  for (let i = pan.length - 1, double = false; i >= 0; i--, double = !double) {
    let digit = Number(pan[i]);
    if (double) { digit *= 2; if (digit > 9) digit -= 9; }
    sum += digit;
  }
  return sum % 10 === 0 ? '' : 'Номер карты не проходит проверку контрольной суммы. Проверьте все цифры номера.';
}
