// Package exclusion holds a list of rules that leave matching items out, and
// counts how many items each rule matches.
//
// A [Rule] is conditions ANDed, and a list of rules is ORed: an item is left
// out when every condition of any one rule holds for it. The app declares its
// [Field]s and answers each item's values through [Item]; [Validate] checks a
// list for a settings save, [Compile] builds the [Set] that matches and
// counts, and [Endpoint] declares the row that counts a draft list.
package exclusion
