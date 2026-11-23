package ui

import "time"

// CzechNameDay returns the Czech name day (svátek / státní svátek) for the given date.
func CzechNameDay(t time.Time) string {
	m := int(t.Month())
	d := t.Day()
	if m < 1 || m > 12 {
		return ""
	}
	days := czechNameDays[m-1]
	if d < 1 || d > len(days) {
		return ""
	}
	return days[d-1]
}

var czechNameDays = [12][]string{
	// Leden
	{
		"Nový rok", "Karina", "Radmila", "Diana", "Dalimil", "Tři králové", "Vilma", "Čestmír", "Vladan", "Břetislav",
		"Bohdana", "Pravoslav", "Edita", "Radovan", "Alice", "Ctirad", "Drahoslav", "Vladislav", "Doubravka", "Ilona",
		"Běla", "Slavomír", "Zdeněk", "Milena", "Miloš", "Zora", "Ingrid", "Otýlie", "Zdislava", "Robin", "Marika",
	},
	// Únor
	{
		"Hynek", "Nela", "Blažej", "Jarmila", "Dobromila", "Vanda", "Veronika", "Milada", "Apolena", "Mojmír",
		"Božena", "Slavěna", "Věnceslav", "Valentýn", "Jiřina", "Ljuba", "Miloslava", "Gizela", "Patrik", "Oldřich",
		"Lenka", "Petr", "Svatopluk", "Matěj", "Liliana", "Dorota", "Alexandr", "Lumír", "Horymír",
	},
	// Březen
	{
		"Bedřich", "Anežka", "Kamil", "Stela", "Kazimír", "Miroslav", "Tomáš", "Gabriela", "Františka", "Viktorie",
		"Anděla", "Řehoř", "Růžena", "Rút a Matylda", "Ida", "Elena a Herbert", "Vlastimil", "Eduard", "Josef", "Světlana",
		"Radek", "Leona", "Ivona", "Gabriel", "Marián", "Emanuel", "Dita", "Soňa", "Taťána", "Arnošt", "Kvido",
	},
	// Duben
	{
		"Hugo", "Erika", "Richard", "Ivana", "Miroslava", "Vendula", "Heřman a Hermína", "Ema", "Dušan", "Darja",
		"Izabela", "Julius", "Aleš", "Vincenc", "Anastázie", "Irena", "Rudolf", "Valérie", "Rostislav", "Marcela",
		"Alexandra", "Evženie", "Vojtěch", "Jiří", "Marek", "Oto", "Jaroslav", "Vlastislav", "Robert", "Blahoslav",
	},
	// Květen
	{
		"Svátek práce", "Zikmund", "Alexej", "Květoslav", "Klaudie", "Radoslav", "Stanislav", "Den vítězství", "Ctibor", "Blažena",
		"Svatava", "Pankrác", "Servác", "Bonifác", "Žofie", "Přemysl", "Aneta", "Nataša", "Ivo", "Zbyšek",
		"Monika", "Emil", "Vladimír", "Jana", "Viola", "Filip", "Valdemar", "Vilém", "Maxmilián", "Ferdinand", "Kamila",
	},
	// Červen
	{
		"Laura", "Jarmil", "Tamara", "Dalibor", "Dobroslav", "Norbert", "Iveta a Slavoj", "Medard", "Stanislava", "Gita",
		"Bruno", "Antonie", "Antonín", "Roland", "Vít", "Zbyněk", "Adolf", "Milan", "Leoš", "Květa",
		"Alois", "Pavla", "Zdeňka", "Jan", "Ivan", "Adriana", "Ladislav", "Lubomír", "Petr a Pavel", "Šárka",
	},
	// Červenec
	{
		"Jaroslava", "Patricie", "Radomír", "Prokop", "Cyril a Metoděj", "Jan Hus", "Bohuslava", "Nora", "Drahoslava", "Libuše a Amálie",
		"Olga", "Bořek", "Markéta", "Karolína", "Jindřich", "Luboš", "Martina", "Drahomíra", "Čeněk", "Ilja",
		"Vítězslav", "Magdaléna", "Libor", "Kristýna", "Jakub", "Anna", "Věroslav", "Viktor", "Marta", "Bořivoj", "Ignác",
	},
	// Srpen
	{
		"Oskar", "Gustav", "Miluše", "Dominik", "Kristián", "Oldřiška", "Lada", "Soběslav", "Roman", "Vavřinec",
		"Zuzana", "Klára", "Alena", "Alan", "Hana", "Jáchym", "Petra", "Helena", "Ludvík", "Bernard",
		"Johana", "Bohuslav", "Sandra", "Bartoloměj", "Radim", "Luděk", "Otakar", "Augustýn", "Evelína", "Vladěna", "Pavlína",
	},
	// Září
	{
		"Linda a Samuel", "Adéla", "Bronislav", "Jindřiška", "Boris", "Boleslav", "Regína", "Mariana", "Daniela", "Irma",
		"Denisa", "Marie", "Lubor", "Radka", "Jolana", "Ludmila", "Naděžda", "Kryštof", "Zita", "Oleg",
		"Matouš", "Darina", "Berta", "Jaromír", "Zlata", "Andrea", "Jonáš", "Václav", "Michal", "Jeronym",
	},
	// Říjen
	{
		"Igor", "Olivie a Oliver", "Bohumil", "František", "Eliška", "Hanuš", "Justýna", "Věra", "Štefan a Sára", "Marina",
		"Andrej", "Marcel", "Renáta", "Agáta", "Tereza", "Havel", "Hedvika", "Lukáš", "Michaela", "Vendelín",
		"Brigita", "Sabina", "Teodor", "Nina", "Beáta", "Erik", "Šarlota a Zoe", "Den vzniku ČSR", "Silvie", "Tadeáš", "Štěpánka",
	},
	// Listopad
	{
		"Felix", "Památka zesnulých", "Hubert", "Karel", "Miriam", "Liběna", "Saskie", "Bohumír", "Bohdan", "Evžen",
		"Martin", "Benedikt", "Tibor", "Sáva", "Leopold", "Otmar", "Mahulena", "Romana", "Alžběta", "Nikola",
		"Albert", "Cecílie", "Klement", "Emílie", "Kateřina", "Artur", "Xenie", "René", "Zina", "Ondřej",
	},
	// Prosinec
	{
		"Iva", "Blanka", "Svatoslav", "Barbora", "Jitka", "Mikuláš", "Ambrož a Benjamín", "Květoslava", "Vratislav", "Julie",
		"Dana", "Simona", "Lucie", "Lýdie", "Radana", "Albína", "Daniel", "Miloslav", "Ester", "Dagmar",
		"Natálie", "Šimon", "Vlasta", "Adam a Eva", "1. svátek vánoční", "Štěpán", "Žaneta", "Bohumila", "Judita", "David", "Silvestr",
	},
}
